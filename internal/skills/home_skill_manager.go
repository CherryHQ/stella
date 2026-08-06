package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// HomeSkillFileInput represents caller-owned binary content. A nil Mode uses
// the ordinary file default (0644); a non-nil Mode is retained exactly.
type HomeSkillFileInput struct {
	Path    string
	Content []byte
	Mode    *fs.FileMode
}

// HomeSkillCreateRequest is a complete desired tree at one exact typed Home
// catalog root. Scope and owners select the root; they are never inferred from
// directory content.
type HomeSkillCreateRequest struct {
	Scope                  string
	UserID                 string
	AgentID                string
	Name                   string
	Description            string
	DisableModelInvocation bool
	Metadata               map[string]any
	Files                  []HomeSkillFileInput
}

// HomeSkillUpdateRequest changes one canonical filesystem Skill ID. Metadata,
// Description, and DisableModelInvocation are optional replacement fields;
// file upserts and deletions are merged into one complete revision.
type HomeSkillUpdateRequest struct {
	ID                     string
	ExpectedDigest         string
	Description            *string
	DisableModelInvocation *bool
	Metadata               *map[string]any
	FileUpserts            []HomeSkillFileInput
	DeleteFiles            []string
	ConvertToManual        bool
}

// HomeSkillManager is the Home-authoritative mutation boundary. It has no
// database, host-path, controller, fallback, retry, or repair dependency.
type HomeSkillManager struct {
	catalog   *HomeCatalog
	publisher *HomeSkillPublisher
	now       func() time.Time
}

func NewHomeSkillManager(catalog *HomeCatalog, publisher *HomeSkillPublisher, now func() time.Time) (*HomeSkillManager, error) {
	if catalog == nil || publisher == nil || now == nil {
		return nil, errors.New("skills: Home Skill manager requires catalog, publisher, and clock")
	}
	return &HomeSkillManager{catalog: catalog, publisher: publisher, now: now}, nil
}

func (m *HomeSkillManager) Create(ctx context.Context, request HomeSkillCreateRequest) (SkillSnapshot, error) {
	if m == nil || m.catalog == nil || m.publisher == nil || m.now == nil {
		return SkillSnapshot{}, errors.New("skills: Home Skill manager is unavailable")
	}
	root, err := homeCatalogSkillRoot(HomeCatalogRoot{Scope: request.Scope, UserID: request.UserID, AgentID: request.AgentID})
	if err != nil {
		return SkillSnapshot{}, err
	}
	if err := skillNameValidationError(request.Name, request.Name); err != nil {
		return SkillSnapshot{}, err
	}
	description := strings.TrimSpace(request.Description)
	if description == "" {
		return SkillSnapshot{}, errors.New("skills: description is required")
	}
	metadata, err := managedCreateMetadata(request.Metadata)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if err := validateHomeCatalogMetadataMap(metadata); err != nil {
		return SkillSnapshot{}, err
	}
	files, err := completeInputFiles(request.Files)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if err := canonicalizeSkillMainFile(files, request.Name, description); err != nil {
		return SkillSnapshot{}, err
	}
	now := m.now().UTC()
	tree := skillTree{Metadata: skillMetadataEnvelope{Status: SkillStatusActive, DisableModelInvocation: request.DisableModelInvocation, Metadata: metadata, CreatedAt: now, UpdatedAt: now, LegacyLifecycleVersion: skillMetadataDefaultLifecycle}, Files: files}
	if err := validateHomeCatalogMetadata(tree.Metadata); err != nil {
		return SkillSnapshot{}, err
	}
	if err := validateHomeSkillPublicationBounds(tree); err != nil {
		return SkillSnapshot{}, err
	}
	digest, err := m.publisher.Publish(ctx, HomeSkillPublishRequest{Root: root, Name: request.Name, Metadata: homeSkillMetadata(tree.Metadata), Files: homeSkillFiles(tree.Files)})
	if err != nil {
		return SkillSnapshot{}, err
	}
	id, _ := encodeFilesystemSkillID(request.Scope, request.UserID, request.AgentID, request.Name)
	return skillSnapshotFromTree(id, request.Scope, request.UserID, request.AgentID, request.Name, description, digest, tree), nil
}

func (m *HomeSkillManager) Update(ctx context.Context, request HomeSkillUpdateRequest) (SkillSnapshot, error) {
	if m == nil || m.catalog == nil || m.publisher == nil || m.now == nil {
		return SkillSnapshot{}, errors.New("skills: Home Skill manager is unavailable")
	}
	snapshotRequest, err := snapshotHomeSkillUpdateRequest(request)
	if err != nil {
		return SkillSnapshot{}, err
	}
	before, err := m.catalog.LoadManagedSnapshot(ctx, snapshotRequest.ID)
	if errors.Is(err, fs.ErrNotExist) {
		return SkillSnapshot{}, ErrHomeSkillConflict
	}
	if err != nil {
		return SkillSnapshot{}, err
	}
	if before.ContentDigest != snapshotRequest.ExpectedDigest {
		return SkillSnapshot{}, ErrHomeSkillConflict
	}
	if before.Skill.Status != SkillStatusActive {
		return SkillSnapshot{}, ErrSkillNotMutable
	}
	if err := skillNameValidationError(before.Skill.Name, before.Skill.Name); err != nil {
		return SkillSnapshot{}, ErrSkillNotMutable
	}
	metadata, err := managedUpdateHomeMetadata(before.Skill.Metadata, snapshotRequest.Metadata, snapshotRequest.ConvertToManual)
	if err != nil {
		return SkillSnapshot{}, err
	}
	description := before.Skill.Description
	if snapshotRequest.Description != nil {
		description = strings.TrimSpace(*snapshotRequest.Description)
	}
	if description == "" {
		return SkillSnapshot{}, errors.New("skills: description is required")
	}
	disable := before.Skill.DisableModelInvocation
	if snapshotRequest.DisableModelInvocation != nil {
		disable = *snapshotRequest.DisableModelInvocation
	}
	files, err := mergeHomeSkillFiles(before.Files, snapshotRequest.FileUpserts, snapshotRequest.DeleteFiles)
	if err != nil {
		return SkillSnapshot{}, err
	}
	if err := canonicalizeSkillMainFile(files, before.Skill.Name, description); err != nil {
		return SkillSnapshot{}, err
	}
	now := m.now().UTC() // One desired revision gets one timestamp, never a retry timestamp.
	tree := skillTree{Metadata: skillMetadataEnvelope{Status: SkillStatusActive, DisableModelInvocation: disable, Metadata: metadata, CreatedAt: before.Skill.CreatedAt.UTC(), UpdatedAt: now, LegacyLifecycleVersion: before.Skill.Version}, Files: files}
	if err := validateHomeCatalogMetadata(tree.Metadata); err != nil {
		return SkillSnapshot{}, err
	}
	if err := validateHomeSkillPublicationBounds(tree); err != nil {
		return SkillSnapshot{}, err
	}
	root, err := homeCatalogSkillRoot(HomeCatalogRoot{Scope: before.Skill.Scope, UserID: before.Skill.UserID, AgentID: before.Skill.AgentID})
	if err != nil {
		return SkillSnapshot{}, ErrSkillNotMutable
	}
	digest, err := m.publisher.Publish(ctx, HomeSkillPublishRequest{Root: root, Name: before.Skill.Name, ExpectedDigest: snapshotRequest.ExpectedDigest, Metadata: homeSkillMetadata(tree.Metadata), Files: homeSkillFiles(tree.Files)})
	if err != nil {
		return SkillSnapshot{}, err
	}
	return skillSnapshotFromTree(before.Skill.ID, before.Skill.Scope, before.Skill.UserID, before.Skill.AgentID, before.Skill.Name, description, digest, tree), nil
}

// DeleteFile is deliberately only a narrow spelling of Update, so a file
// deletion cannot become a separately published intermediate revision.
func (m *HomeSkillManager) DeleteFile(ctx context.Context, id, expectedDigest, filename string) (SkillSnapshot, error) {
	return m.Update(ctx, HomeSkillUpdateRequest{ID: id, ExpectedDigest: expectedDigest, DeleteFiles: []string{filename}})
}

// Delete withdraws the direct managed selection while retaining its immutable
// revision. Unpublish owns the expected-digest check and unknown-outcome rules.
func (m *HomeSkillManager) Delete(ctx context.Context, id, expectedDigest string) error {
	if m == nil || m.catalog == nil || m.publisher == nil {
		return errors.New("skills: Home Skill manager is unavailable")
	}
	if err := validateHomeSkillDelete(id, expectedDigest); err != nil {
		return err
	}
	before, err := m.catalog.LoadManagedSnapshot(ctx, id)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrHomeSkillConflict
	}
	if err != nil {
		return err
	}
	if before.ContentDigest != expectedDigest {
		return ErrHomeSkillConflict
	}
	if before.Skill.Status != SkillStatusActive {
		return ErrSkillNotMutable
	}
	root, err := homeCatalogSkillRoot(HomeCatalogRoot{Scope: before.Skill.Scope, UserID: before.Skill.UserID, AgentID: before.Skill.AgentID})
	if err != nil {
		return ErrSkillNotMutable
	}
	return m.publisher.Unpublish(ctx, HomeSkillUnpublishRequest{Root: root, Name: before.Skill.Name, ExpectedDigest: expectedDigest})
}

func snapshotHomeSkillUpdateRequest(request HomeSkillUpdateRequest) (HomeSkillUpdateRequest, error) {
	if _, _, _, _, err := decodeFilesystemSkillID(request.ID); err != nil {
		return HomeSkillUpdateRequest{}, err
	}
	if !validHomeSkillDigest(request.ExpectedDigest) {
		return HomeSkillUpdateRequest{}, errors.New("skills: expected digest must be a lowercase SHA-256 digest")
	}
	if request.Description != nil {
		value := *request.Description
		request.Description = &value
	}
	if request.DisableModelInvocation != nil {
		value := *request.DisableModelInvocation
		request.DisableModelInvocation = &value
	}
	if request.Metadata != nil {
		copied, err := copyHomeSkillMetadata(*request.Metadata)
		if err != nil {
			return HomeSkillUpdateRequest{}, err
		}
		if createdBy, exists := copied[reflectSkillCreatedByKey]; exists && !validManagedCreatedBy(createdBy) {
			return HomeSkillUpdateRequest{}, errors.New("skills: metadata.created_by must be manual or reflect")
		}
		if err := validateHomeCatalogMetadataMap(copied); err != nil {
			return HomeSkillUpdateRequest{}, err
		}
		request.Metadata = &copied
	}
	files, err := snapshotHomeSkillFileInputs(request.FileUpserts)
	if err != nil {
		return HomeSkillUpdateRequest{}, err
	}
	request.FileUpserts = files
	deletions := append([]string(nil), request.DeleteFiles...)
	seen := make(map[string]struct{}, len(deletions))
	for _, filename := range deletions {
		if err := validateHomeMutationPath(filename); err != nil {
			return HomeSkillUpdateRequest{}, err
		}
		if filename == MainFile {
			return HomeSkillUpdateRequest{}, errors.New("skills: SKILL.md cannot be deleted")
		}
		if _, duplicate := seen[filename]; duplicate {
			return HomeSkillUpdateRequest{}, fmt.Errorf("skills: duplicate Skill file deletion %q", filename)
		}
		seen[filename] = struct{}{}
	}
	for _, file := range files {
		if _, deleted := seen[file.Path]; deleted {
			return HomeSkillUpdateRequest{}, fmt.Errorf("skills: Skill file %q is both upserted and deleted", file.Path)
		}
	}
	request.DeleteFiles = deletions
	return request, nil
}

func validateHomeSkillDelete(id, expectedDigest string) error {
	if _, _, _, _, err := decodeFilesystemSkillID(id); err != nil {
		return err
	}
	if !validHomeSkillDigest(expectedDigest) {
		return errors.New("skills: expected digest must be a lowercase SHA-256 digest")
	}
	return nil
}

func completeInputFiles(input []HomeSkillFileInput) ([]skillTreeEntry, error) {
	files, err := snapshotHomeSkillFileInputs(input)
	if err != nil {
		return nil, err
	}
	out := make([]skillTreeEntry, len(files))
	for i, file := range files {
		mode := fs.FileMode(0o644)
		if file.Mode != nil {
			mode = *file.Mode
		}
		out[i] = skillTreeEntry{Path: file.Path, Content: file.Content, Mode: mode}
	}
	if err := validateSkillTreeFiles(out); err != nil {
		return nil, err
	}
	return out, nil
}

func snapshotHomeSkillFileInputs(input []HomeSkillFileInput) ([]HomeSkillFileInput, error) {
	files := make([]HomeSkillFileInput, len(input))
	seen := make(map[string]struct{}, len(input))
	for i, file := range input {
		if err := validateHomeMutationPath(file.Path); err != nil {
			return nil, err
		}
		if len(file.Content) > maxManagedFileBytes {
			return nil, fmt.Errorf("skills: Skill file %q exceeds file limit", file.Path)
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return nil, fmt.Errorf("skills: duplicate Skill file path %q", file.Path)
		}
		seen[file.Path] = struct{}{}
		files[i] = HomeSkillFileInput{Path: file.Path, Content: append([]byte(nil), file.Content...)}
		if file.Mode != nil {
			mode := *file.Mode
			if mode&^fs.FileMode(0o777) != 0 {
				return nil, fmt.Errorf("skills: Skill file %q is not regular", file.Path)
			}
			files[i].Mode = &mode
		}
	}
	return files, nil
}

func validateHomeMutationPath(filename string) error {
	if err := validateSkillTreePath(filename); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSkillFilePath, err)
	}
	return nil
}

func mergeHomeSkillFiles(before []HomeSkillFile, upserts []HomeSkillFileInput, deletions []string) ([]skillTreeEntry, error) {
	files := make(map[string]skillTreeEntry, len(before)+len(upserts))
	for _, file := range before {
		files[file.Path] = skillTreeEntry{Path: file.Path, Content: append([]byte(nil), file.Content...), Mode: file.Mode}
	}
	for _, filename := range deletions {
		delete(files, filename)
	}
	for _, file := range upserts {
		mode := fs.FileMode(0o644)
		if existing, exists := files[file.Path]; exists {
			mode = existing.Mode
		}
		if file.Mode != nil {
			mode = *file.Mode
		}
		files[file.Path] = skillTreeEntry{Path: file.Path, Content: append([]byte(nil), file.Content...), Mode: mode}
	}
	out := make([]skillTreeEntry, 0, len(files))
	for _, file := range files {
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if err := validateSkillTreeFiles(out); err != nil {
		return nil, err
	}
	return out, nil
}

func managedCreateMetadata(metadata map[string]any) (map[string]any, error) {
	copied, err := copyHomeSkillMetadata(metadata)
	if err != nil {
		return nil, err
	}
	createdBy, exists := copied[reflectSkillCreatedByKey]
	if !exists {
		copied[reflectSkillCreatedByKey] = ManualSkillCreatedBy
		return copied, nil
	}
	if !validManagedCreatedBy(createdBy) {
		return nil, errors.New("skills: metadata.created_by must be manual or reflect")
	}
	return copied, nil
}

func managedUpdateHomeMetadata(existingJSON []byte, patch *map[string]any, convertToManual bool) (map[string]any, error) {
	existingValue, err := decodeStrictJSON(existingJSON)
	if err != nil {
		return nil, fmt.Errorf("skills: decode managed Skill metadata: %w", err)
	}
	existing, ok := existingValue.(map[string]any)
	if !ok || !validManagedCreatedBy(existing[reflectSkillCreatedByKey]) {
		return nil, ErrSkillNotMutable
	}
	owner := existing[reflectSkillCreatedByKey].(string)
	if convertToManual && owner != ReflectSkillCreatedBy {
		return nil, ErrSkillNotReflectOwned
	}
	metadata := existing
	if patch != nil {
		metadata, err = copyHomeSkillMetadata(*patch)
		if err != nil {
			return nil, err
		}
		if requestedOwner, present := metadata[reflectSkillCreatedByKey]; present && (!validManagedCreatedBy(requestedOwner) || requestedOwner.(string) != owner) && !convertToManual {
			return nil, errors.New("skills: metadata.created_by cannot change without ConvertToManual")
		}
	}
	if convertToManual {
		metadata[reflectSkillCreatedByKey] = ManualSkillCreatedBy
	} else {
		metadata[reflectSkillCreatedByKey] = owner
	}
	return metadata, nil
}

func validManagedCreatedBy(value any) bool {
	createdBy, ok := value.(string)
	return ok && (createdBy == ManualSkillCreatedBy || createdBy == ReflectSkillCreatedBy)
}

func copyHomeSkillMetadata(metadata map[string]any) (map[string]any, error) {
	if metadata == nil {
		return map[string]any{}, nil
	}
	encoded, err := canonicalJSON(metadata)
	if err != nil {
		return nil, fmt.Errorf("skills: invalid Home Skill metadata: %w", err)
	}
	value, err := decodeStrictJSON(encoded)
	if err != nil {
		return nil, fmt.Errorf("skills: copy Home Skill metadata: %w", err)
	}
	copied, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("skills: Home Skill metadata must be an object")
	}
	return copied, nil
}

// validateHomeCatalogMetadataMap is conservative before a Home read: an
// update may inherit either legal owner, so size it with the longer marker.
func validateHomeCatalogMetadataMap(metadata map[string]any) error {
	copied, err := copyHomeSkillMetadata(metadata)
	if err != nil {
		return err
	}
	copied[reflectSkillCreatedByKey] = ReflectSkillCreatedBy
	return validateHomeCatalogMetadata(skillMetadataEnvelope{
		Status:                 SkillStatusActive,
		Metadata:               copied,
		CreatedAt:              time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		UpdatedAt:              time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		LegacyLifecycleVersion: 1,
	})
}

func validateHomeCatalogMetadata(metadata skillMetadataEnvelope) error {
	encoded, err := encodeSkillMetadataEnvelope(metadata)
	if err != nil {
		return fmt.Errorf("skills: invalid Home Skill metadata: %w", err)
	}
	if len(encoded) > maxCatalogMetadataBytes {
		return errors.New("skills: Home Skill metadata exceeds catalog limit")
	}
	return nil
}

func homeSkillMetadata(metadata skillMetadataEnvelope) HomeSkillMetadata {
	return HomeSkillMetadata(metadata)
}

func homeSkillFiles(files []skillTreeEntry) []HomeSkillFile {
	out := make([]HomeSkillFile, len(files))
	for i, file := range files {
		out[i] = HomeSkillFile(file)
	}
	return out
}

func skillSnapshotFromTree(id, scope, userID, agentID, name, description, digest string, tree skillTree) SkillSnapshot {
	metadata, _ := canonicalJSON(tree.Metadata.Metadata)
	files := make([]string, 0, len(tree.Files))
	for _, file := range tree.Files {
		files = append(files, file.Path)
	}
	sort.Strings(files)
	return SkillSnapshot{Skill: Skill{ID: id, Scope: scope, UserID: userID, AgentID: agentID, Name: name, Description: description, Status: tree.Metadata.Status, DisableModelInvocation: tree.Metadata.DisableModelInvocation, Metadata: metadata, CreatedAt: tree.Metadata.CreatedAt, UpdatedAt: tree.Metadata.UpdatedAt, Version: tree.Metadata.LegacyLifecycleVersion, ContentDigest: digest}, Files: files}
}

// canonicalizeSkillMainFile changes only the two authority fields in YAML
// frontmatter. YAML nodes retain unknown keys while body bytes are copied from
// the source untouched; this avoids a metadata/body split without inventing a
// second SKILL.md format.
func canonicalizeSkillMainFile(files []skillTreeEntry, name, description string) error {
	for i := range files {
		if files[i].Path != MainFile {
			continue
		}
		content, err := rewriteSkillFrontmatter(files[i].Content, name, description)
		if err != nil {
			return err
		}
		files[i].Content = content
		return nil
	}
	return errors.New("skills: skill tree is missing SKILL.md")
}

func rewriteSkillFrontmatter(content []byte, name, description string) ([]byte, error) {
	if len(content) == 0 {
		return nil, errors.New("skills: SKILL.md must not be empty")
	}
	frontmatter, body, err := splitSkillFrontmatter(content)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(frontmatter, &document); err != nil {
		return nil, fmt.Errorf("skills: invalid SKILL.md frontmatter: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("skills: SKILL.md frontmatter must be a mapping")
	}
	mapping := document.Content[0]
	seen := make(map[string]struct{}, len(mapping.Content)/2)
	var descriptionNode *yaml.Node
	for i := 0; i < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Value == "" {
			return nil, errors.New("skills: SKILL.md frontmatter has an invalid key")
		}
		if _, duplicate := seen[key.Value]; duplicate {
			return nil, fmt.Errorf("skills: SKILL.md frontmatter repeats %q", key.Value)
		}
		seen[key.Value] = struct{}{}
		if key.Value == "description" {
			descriptionNode = value
		}
	}
	if descriptionNode == nil || descriptionNode.Kind != yaml.ScalarNode || strings.TrimSpace(descriptionNode.Value) == "" {
		return nil, errors.New("skills: SKILL.md frontmatter description is required")
	}
	setSkillFrontmatterField(mapping, "name", name)
	setSkillFrontmatterField(mapping, "description", description)
	encoded, err := yaml.Marshal(mapping)
	if err != nil {
		return nil, fmt.Errorf("skills: encode SKILL.md frontmatter: %w", err)
	}
	return bytes.Join([][]byte{[]byte("---\n"), encoded, []byte("---"), body}, nil), nil
}

func splitSkillFrontmatter(content []byte) ([]byte, []byte, error) {
	lineEnd := bytes.IndexByte(content, '\n')
	if lineEnd < 0 || !bytes.Equal(bytes.TrimSuffix(content[:lineEnd], []byte("\r")), []byte("---")) {
		return nil, nil, errors.New("skills: SKILL.md has no frontmatter")
	}
	start := lineEnd + 1
	for cursor := start; cursor <= len(content); {
		relativeEnd := bytes.IndexByte(content[cursor:], '\n')
		end := len(content)
		next := len(content)
		if relativeEnd >= 0 {
			end = cursor + relativeEnd
			next = end + 1
		}
		if bytes.Equal(bytes.TrimSuffix(content[cursor:end], []byte("\r")), []byte("---")) {
			// Retain the delimiter's line ending as the structural separator;
			// body bytes after it stay exactly where the caller put them.
			return content[start:cursor], content[end:], nil
		}
		if relativeEnd < 0 {
			break
		}
		cursor = next
	}
	return nil, nil, errors.New("skills: SKILL.md has no closing frontmatter delimiter")
}

func setSkillFrontmatterField(mapping *yaml.Node, key, value string) {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}
