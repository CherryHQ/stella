package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// MaterializedPackage is a complete package tree ready for atomic publication.
// Files are relative to the package directory and are sorted by path.
type MaterializedPackage struct {
	Files  []ResourceFileEntry
	Digest string
}

type ResourceFileEntry struct {
	Path string
	ResourceFile
}

// MCPMigrationEvidence maps one old database child to its new file identity.
// It deliberately carries no credential value or Vault payload.
type MCPMigrationEvidence struct {
	OldConfigID   string
	OldServerKey  string
	NewResource   ResourceKey
	OldCredential string
}

// MaterializedRemoteMCP is a standalone mcp/<name>.json file plus the
// non-secret mapping needed by the migration coordinator.
type MaterializedRemoteMCP struct {
	Name     string
	File     ResourceFileEntry
	Evidence MCPMigrationEvidence
}

// MaterializePackage converts one legacy Definition/Config pair into a
// complete standard package. source is the already captured custom package;
// builtin skills are read through builtinReader and never through a CAS path.
func MaterializePackage(ctx context.Context, definition Definition, config *Config, source map[string]ResourceFile, builtinReader BuiltinSkillReader) (MaterializedPackage, error) {
	var credentialRefs json.RawMessage
	if config != nil {
		credentialRefs = config.CredentialRefs
	}
	return materializePackage(ctx, definition, config, credentialRefs, source, builtinReader)
}

// materializePackage carries credential references separately from Config so
// migration can materialize a definition-inherited payload while preserving
// refs authored on a config whose Payload is empty.
func materializePackage(ctx context.Context, definition Definition, config *Config, credentialRefs json.RawMessage, source map[string]ResourceFile, builtinReader BuiltinSkillReader) (MaterializedPackage, error) {
	if err := contextError(ctx); err != nil {
		return MaterializedPackage{}, err
	}
	if definition.Source != SourceBuiltin && definition.Source != SourceCustom {
		return MaterializedPackage{}, fmt.Errorf("plugin: definition %q has invalid source %q", definition.ID, definition.Source)
	}
	payload, err := materializePayload(definition, config)
	if err != nil {
		return MaterializedPackage{}, err
	}
	if payload == nil {
		return MaterializedPackage{}, nil
	}

	files := cloneResourceFiles(source)
	if definition.Source == SourceBuiltin {
		files = make(map[string]ResourceFile)
		if err := materializeBuiltinSkills(ctx, definition.ID, payload.Skills, builtinReader, files); err != nil {
			return MaterializedPackage{}, err
		}
	} else {
		if err := validateSourceFileTree(files); err != nil {
			return MaterializedPackage{}, err
		}
		if err := validateCapturedPackage(definition, payload, files); err != nil {
			return MaterializedPackage{}, err
		}
	}

	manifest, err := materializeManifest(definition, payload, credentialRefs, files["plugin.json"])
	if err != nil {
		return MaterializedPackage{}, err
	}
	files["plugin.json"] = ResourceFile{Data: manifest, Mode: regularMode(files["plugin.json"].Mode)}
	if err := materializeMCPFiles(payload, files); err != nil {
		return MaterializedPackage{}, err
	}

	return MaterializedPackage{Files: resourceFileEntries(files), Digest: digestResourceFiles(files)}, nil
}

func validateSourceFileTree(files map[string]ResourceFile) error {
	for filename, file := range files {
		normalized := strings.ReplaceAll(filename, "\\", "/")
		relative, err := safeRelativePath(filename)
		if err != nil {
			return fmt.Errorf("plugin: source file %q: %w", filename, err)
		}
		if relative != normalized {
			return fmt.Errorf("plugin: source file %q is not a clean relative path", filename)
		}
		if file.Mode.Type() != 0 {
			return fmt.Errorf("plugin: source file %q is not regular", filename)
		}
	}
	return nil
}

// MaterializeRemoteMCP converts a legacy remote-MCP definition into one or
// more independent mcp files. A package identity is never synthesized for it.
func MaterializeRemoteMCP(ctx context.Context, definition Definition, config Config) ([]MaterializedRemoteMCP, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	payload, err := materializePayload(definition, &config)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, nil
	}
	if payload.Origin != "remote_mcp" {
		return nil, fmt.Errorf("plugin: definition %q is not a remote MCP resource", definition.ID)
	}
	keys := slices.Sorted(maps.Keys(payload.MCPServers))
	if len(keys) == 0 {
		return nil, errors.New("plugin: remote MCP definition has no servers")
	}
	used := make(map[string]struct{}, len(keys))
	result := make([]MaterializedRemoteMCP, 0, len(keys))
	for _, serverKey := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, err := materializedMCPName(definition.ID, serverKey, len(keys) > 1, used)
		if err != nil {
			return nil, err
		}
		resourceKey := ResourceKey{Scope: config.Scope, UserID: config.UserID, AgentID: config.AgentID, Kind: ResourceMCP, Name: name}
		if resourceKey.ID() == "" {
			return nil, fmt.Errorf("plugin: remote MCP %q has invalid owner scope", serverKey)
		}
		declaration, err := declarationFromPayload(*payload, serverKey, config.CredentialRefs)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(declaration)
		if err != nil {
			return nil, fmt.Errorf("plugin: encode MCP %q: %w", serverKey, err)
		}
		result = append(result, MaterializedRemoteMCP{
			Name: name,
			File: ResourceFileEntry{Path: path.Join("mcp", name+".json"), ResourceFile: ResourceFile{Data: data, Mode: 0o644}},
			Evidence: MCPMigrationEvidence{
				OldConfigID: config.ID, OldServerKey: serverKey,
				NewResource:   resourceKey,
				OldCredential: credentialLocatorName(config.CredentialRefs, serverKey),
			},
		})
	}
	return result, nil
}

func materializePayload(definition Definition, config *Config) (*ResourcePayload, error) {
	if config != nil && len(config.Payload) == 0 {
		if config.Enabled != nil && *config.Enabled {
			return nil, fmt.Errorf("plugin: enabled config %q has no payload", config.ID)
		}
		return nil, nil
	}
	raw := definition.Spec
	var err error
	if config != nil {
		if config.PluginID != "" && config.PluginID != definition.ID {
			return nil, fmt.Errorf("plugin: config %q does not match definition %q", config.PluginID, definition.ID)
		}
		raw, err = MergeDefinitionConfig(definition.Spec, config.Payload)
		if err != nil {
			return nil, fmt.Errorf("plugin: merge definition %q: %w", definition.ID, err)
		}
	}
	var payload ResourcePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("plugin: decode definition %q: %w", definition.ID, err)
	}
	return &payload, nil
}

func materializeBuiltinSkills(ctx context.Context, pluginID string, skills []SkillResource, reader BuiltinSkillReader, files map[string]ResourceFile) error {
	if len(skills) != 0 && reader == nil {
		return errors.New("plugin: builtin Skill reader unavailable")
	}
	for _, skill := range skills {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !agentpackage.ValidSkillName(skill.Name) {
			return fmt.Errorf("plugin: invalid builtin Skill name %q", skill.Name)
		}
		content, modes, err := reader(ctx, pluginID, skill.Name)
		if err != nil {
			return fmt.Errorf("plugin: read builtin Skill %q: %w", skill.Name, err)
		}
		if len(content) == 0 || len(content) != len(modes) {
			return fmt.Errorf("plugin: builtin Skill %q has incomplete files", skill.Name)
		}
		for filename, data := range content {
			if err := ctx.Err(); err != nil {
				return err
			}
			relative, err := safeRelativePath(filename)
			if err != nil {
				return fmt.Errorf("plugin: builtin Skill %q file: %w", skill.Name, err)
			}
			mode, ok := modes[filename]
			if !ok {
				return fmt.Errorf("plugin: builtin Skill %q file %q has no mode", skill.Name, filename)
			}
			if !mode.IsRegular() && mode.Type() != 0 {
				return fmt.Errorf("plugin: builtin Skill %q file %q is not regular", skill.Name, filename)
			}
			files[path.Join("skills", skill.Name, relative)] = ResourceFile{Data: bytes.Clone(data), Mode: regularMode(mode)}
		}
	}
	return nil
}

func validateCapturedPackage(definition Definition, payload *ResourcePayload, files map[string]ResourceFile) error {
	manifest, ok := files["plugin.json"]
	if !ok {
		return errors.New("plugin: custom package is missing plugin.json")
	}
	loaded, diagnostics := agentpackage.LoadFS(resourceMapFS(files))
	if loaded == nil || diagnostics.HasErrors() {
		return fmt.Errorf("plugin: custom package is invalid: %s", firstDiagnostic(diagnostics))
	}
	if loaded.Manifest.Name != definition.ID {
		return fmt.Errorf("plugin: package name %q differs from definition %q", loaded.Manifest.Name, definition.ID)
	}
	if manifest.Mode.Type() != 0 || !manifest.Mode.IsRegular() {
		return errors.New("plugin: plugin.json is not regular")
	}
	declared := make(map[string]struct{}, len(payload.Skills))
	for _, skill := range payload.Skills {
		declared[skill.Name] = struct{}{}
	}
	for _, skill := range loaded.Skills {
		if _, ok := declared[skill.Name]; !ok {
			return fmt.Errorf("plugin: package Skill %q is not declared", skill.Name)
		}
	}
	if len(loaded.Skills) != len(declared) {
		return errors.New("plugin: package Skill declaration does not match source tree")
	}
	return nil
}

func materializeManifest(definition Definition, payload *ResourcePayload, credentialRefs json.RawMessage, source ResourceFile) ([]byte, error) {
	var object map[string]json.RawMessage
	if definition.Source == SourceBuiltin {
		object = make(map[string]json.RawMessage)
	} else {
		if len(source.Data) == 0 {
			return nil, errors.New("plugin: plugin.json is empty")
		}
		if err := json.Unmarshal(source.Data, &object); err != nil || object == nil {
			return nil, errors.New("plugin: plugin.json is not an object")
		}
	}
	object["$schema"] = json.RawMessage(strconvQuote(agentpackage.PluginSchemaV1))
	object["name"] = json.RawMessage(strconvQuote(definition.ID))
	if payload.Version != "" {
		object["version"] = json.RawMessage(strconvQuote(payload.Version))
	}
	if payload.Description != "" {
		object["description"] = json.RawMessage(strconvQuote(payload.Description))
	}
	stella, err := materializeStellaExtension(definition, *payload, credentialRefs)
	if err != nil {
		return nil, err
	}
	extensions := map[string]json.RawMessage{}
	if raw, ok := object["extensions"]; ok {
		if err := json.Unmarshal(raw, &extensions); err != nil || extensions == nil {
			return nil, errors.New("plugin: plugin.json extensions is not an object")
		}
	}
	if len(stella) == 0 {
		delete(extensions, agentpackage.StellaNamespace)
	} else {
		extensions[agentpackage.StellaNamespace] = stella
	}
	if len(extensions) == 0 {
		delete(object, "extensions")
	} else {
		encoded, err := json.Marshal(extensions)
		if err != nil {
			return nil, fmt.Errorf("plugin: encode plugin extensions: %w", err)
		}
		object["extensions"] = encoded
	}
	return json.Marshal(object)
}

func materializeStellaExtension(definition Definition, payload ResourcePayload, credentialRefs json.RawMessage) ([]byte, error) {
	extension := map[string]any{"version": agentpackage.StellaExtensionV1}
	if definition.DisplayName != "" {
		extension["display_name"] = definition.DisplayName
	}
	if payload.Prompt != "" {
		extension["prompt"] = payload.Prompt
	}
	if len(payload.Binaries) != 0 {
		extension["binaries"] = payload.Binaries
	}
	if len(payload.SessionEnvs) != 0 {
		for _, env := range payload.SessionEnvs {
			if env.Value != "" {
				return nil, errors.New("plugin: literal session environment values cannot be materialized")
			}
		}
		extension["session_env"] = payload.SessionEnvs
	}
	if len(payload.OAuth) != 0 {
		extension["oauth"] = payload.OAuth
	}
	if len(payload.MCPServers) != 0 {
		auth := make(map[string]mcpconfig.Authentication, len(payload.MCPServers))
		options := make(map[string]mcpconfig.Options, len(payload.MCPServers))
		for key, server := range payload.MCPServers {
			declaration, err := declarationFromPayload(payload, key, credentialRefs)
			if err != nil {
				return nil, err
			}
			auth[key] = declaration.Authentication
			options[key] = mcpconfig.Options{Description: server.Description, CallTimeoutSeconds: mcpTimeout(server.Metadata)}
		}
		extension["mcp_auth"] = auth
		for key, option := range options {
			if option.Description == "" && option.CallTimeoutSeconds == 0 {
				delete(options, key)
			}
		}
		if len(options) != 0 {
			extension["mcp_options"] = options
		}
	}
	if len(extension) == 1 {
		return nil, nil
	}
	data, err := json.Marshal(extension)
	if err != nil {
		return nil, fmt.Errorf("plugin: encode Stella extension: %w", err)
	}
	return data, nil
}

func materializeMCPFiles(payload *ResourcePayload, files map[string]ResourceFile) error {
	if len(payload.MCPServers) == 0 {
		delete(files, "mcp.json")
		return nil
	}
	servers := make(map[string]any, len(payload.MCPServers))
	for _, key := range slices.Sorted(maps.Keys(payload.MCPServers)) {
		declaration, err := declarationFromPayload(*payload, key, nil)
		if err != nil {
			return err
		}
		wireType, ok := map[string]string{"streamable_http": "streamable-http", "sse": "sse"}[declaration.Transport]
		if !ok {
			return fmt.Errorf("plugin: MCP server %q uses unsupported transport %q", key, declaration.Transport)
		}
		server := map[string]any{"type": wireType, "url": declaration.URL}
		if len(declaration.Headers) != 0 {
			server["headers"] = declaration.Headers
		}
		servers[key] = server
	}
	data, err := json.Marshal(map[string]any{"$schema": agentpackage.MCPV1Schema, "mcpServers": servers})
	if err != nil {
		return fmt.Errorf("plugin: encode package MCP: %w", err)
	}
	files["mcp.json"] = ResourceFile{Data: data, Mode: 0o644}
	return nil
}

func declarationFromPayload(payload ResourcePayload, key string, credentialRefs json.RawMessage) (mcpconfig.Declaration, error) {
	server, ok := payload.MCPServers[key]
	if !ok {
		return mcpconfig.Declaration{}, fmt.Errorf("plugin: MCP server %q is not declared", key)
	}
	auth := mcpconfig.Authentication{Type: server.AuthType, Mode: server.CredentialMode}
	if auth.Type == "" {
		auth.Type = "none"
	}
	if auth.Mode == "" {
		auth.Mode = "per_user"
	}
	if metadata, ok := server.Metadata["oauth"].(map[string]any); ok {
		if value, ok := metadata["client_id"].(string); ok {
			auth.ClientID = value
		}
		if value, ok := metadata["token_endpoint_auth_method"].(string); ok {
			auth.TokenEndpointAuthMethod = value
		}
	}
	applyCredentialRefs(&auth, credentialRefs, key)
	declaration := mcpconfig.Declaration{URL: server.URL, Transport: server.Transport, Headers: maps.Clone(server.Headers), Description: server.Description, CallTimeoutSeconds: mcpTimeout(server.Metadata), Authentication: auth}
	normalized, err := mcpconfig.Normalize(declaration)
	if err != nil {
		return mcpconfig.Declaration{}, fmt.Errorf("plugin: MCP server %q: %w", key, err)
	}
	return normalized, nil
}

func applyCredentialRefs(auth *mcpconfig.Authentication, raw json.RawMessage, key string) {
	if len(raw) == 0 {
		return
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return
	}
	if nested, ok := object["mcp_servers"]; ok {
		var servers map[string]json.RawMessage
		if json.Unmarshal(nested, &servers) == nil {
			raw = servers[key]
			if len(raw) == 0 {
				return
			}
			_ = json.Unmarshal(raw, &object)
		}
	}
	var ref map[string]json.RawMessage
	if json.Unmarshal(raw, &ref) != nil {
		return
	}
	if value, ok := ref["bearer"]; ok {
		auth.CredentialRef = locatorName(value)
	}
	if value, ok := ref["oauth_bundle"]; ok {
		auth.CredentialRef = locatorName(value)
	}
	if value, ok := ref["oauth_client_secret"]; ok {
		auth.ClientSecretRef = locatorName(value)
	}
}

func credentialLocatorName(raw json.RawMessage, key string) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	if nested, ok := object["mcp_servers"]; ok {
		var servers map[string]json.RawMessage
		if json.Unmarshal(nested, &servers) != nil {
			return ""
		}
		raw = servers[key]
		if len(raw) == 0 {
			return ""
		}
		if json.Unmarshal(raw, &object) != nil {
			return ""
		}
	}
	for _, field := range []string{"bearer", "oauth_bundle", "oauth_client_secret"} {
		if value, ok := object[field]; ok {
			if name := locatorName(value); name != "" {
				return name
			}
		}
	}
	return ""
}

func locatorName(raw json.RawMessage) string {
	var object struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	return object.Name
}

func materializedMCPName(base, serverKey string, includeKey bool, used map[string]struct{}) (string, error) {
	name := base
	if includeKey && serverKey != "main" {
		canonical, err := canonicalLegacyName(serverKey)
		if err != nil {
			return "", fmt.Errorf("plugin: remote MCP server %q name: %w", serverKey, err)
		}
		name += "-" + canonical
	}
	if !agentpackage.ValidName(name) || strings.Contains(name, "--") || strings.Contains(name, "..") {
		return "", fmt.Errorf("plugin: remote MCP resource name %q is invalid", name)
	}
	if _, exists := used[name]; exists {
		return "", fmt.Errorf("plugin: remote MCP resource name %q collides", name)
	}
	used[name] = struct{}{}
	return name, nil
}

func resourceMapFS(files map[string]ResourceFile) fs.FS {
	result := make(resourceMap, len(files))
	for filename, file := range files {
		result[filename] = resourceMapFile{data: bytes.Clone(file.Data), mode: regularMode(file.Mode)}
	}
	return result
}

// resourceMap is a small captured filesystem used only to validate a package
// after its bytes have already been read. It never resolves host paths.
type resourceMap map[string]resourceMapFile

type resourceMapFile struct {
	data []byte
	mode fs.FileMode
}

func (m resourceMap) Open(name string) (fs.File, error) {
	name = path.Clean(strings.TrimPrefix(name, "./"))
	if name == "." {
		return &resourceMapDir{fsys: m, name: "."}, nil
	}
	if file, ok := m[name]; ok {
		return &resourceMapRegular{file: file, name: path.Base(name)}, nil
	}
	for filename := range m {
		if strings.HasPrefix(filename, name+"/") {
			return &resourceMapDir{fsys: m, name: name}, nil
		}
	}
	return nil, fs.ErrNotExist
}

type resourceMapRegular struct {
	file   resourceMapFile
	name   string
	offset int
}

func (f *resourceMapRegular) Stat() (fs.FileInfo, error) {
	return resourceMapInfo{name: f.name, size: int64(len(f.file.data)), mode: f.file.mode}, nil
}

func (f *resourceMapRegular) Read(data []byte) (int, error) {
	if f.offset >= len(f.file.data) {
		return 0, io.EOF
	}
	n := copy(data, f.file.data[f.offset:])
	f.offset += n
	return n, nil
}
func (f *resourceMapRegular) Close() error { return nil }

type resourceMapDir struct {
	fsys   resourceMap
	name   string
	offset int
}

func (d *resourceMapDir) Stat() (fs.FileInfo, error) {
	return resourceMapInfo{name: path.Base(d.name), mode: fs.ModeDir | 0o755}, nil
}

func (d *resourceMapDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: errors.New("is a directory")}
}
func (d *resourceMapDir) Close() error { return nil }
func (d *resourceMapDir) ReadDir(n int) ([]fs.DirEntry, error) {
	seen := map[string]struct{}{}
	prefix := strings.TrimPrefix(d.name, "./")
	if prefix != "" && prefix != "." {
		prefix += "/"
	} else {
		prefix = ""
	}
	for filename := range d.fsys {
		if !strings.HasPrefix(filename, prefix) {
			continue
		}
		part := strings.Split(strings.TrimPrefix(filename, prefix), "/")[0]
		if part != "" {
			seen[part] = struct{}{}
		}
	}
	entries := make([]fs.DirEntry, 0, len(seen))
	for name := range seen {
		candidate := name
		if prefix != "" {
			candidate = strings.TrimSuffix(prefix, "/") + "/" + name
		}
		isDir := false
		for filename := range d.fsys {
			if strings.HasPrefix(filename, candidate+"/") {
				isDir = true
				break
			}
		}
		entries = append(entries, resourceMapDirEntry{name: name, dir: isDir})
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	if d.offset >= len(entries) {
		if n > 0 {
			return nil, io.EOF
		}
		return nil, nil
	}
	if n > 0 && len(entries)-d.offset > n {
		result := entries[d.offset : d.offset+n]
		d.offset += n
		return result, nil
	}
	result := entries[d.offset:]
	d.offset = len(entries)
	return result, nil
}

type resourceMapInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (f resourceMapInfo) Name() string       { return f.name }
func (f resourceMapInfo) Size() int64        { return f.size }
func (f resourceMapInfo) Mode() fs.FileMode  { return f.mode }
func (f resourceMapInfo) ModTime() time.Time { return time.Time{} }
func (f resourceMapInfo) IsDir() bool        { return f.mode.IsDir() }
func (f resourceMapInfo) Sys() any           { return nil }

type resourceMapDirEntry struct {
	name string
	dir  bool
}

func (e resourceMapDirEntry) Name() string { return e.name }
func (e resourceMapDirEntry) IsDir() bool  { return e.dir }
func (e resourceMapDirEntry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0
}

func (e resourceMapDirEntry) Info() (fs.FileInfo, error) {
	mode := fs.FileMode(0)
	if e.dir {
		mode = fs.ModeDir | 0o755
	}
	return resourceMapInfo{name: e.name, mode: mode}, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("plugin: materialization context is nil")
	}
	return ctx.Err()
}

func cloneResourceFiles(files map[string]ResourceFile) map[string]ResourceFile {
	result := make(map[string]ResourceFile, len(files))
	for filename, file := range files {
		result[filename] = ResourceFile{Data: bytes.Clone(file.Data), Mode: file.Mode}
	}
	return result
}

func resourceFileEntries(files map[string]ResourceFile) []ResourceFileEntry {
	paths := slices.Sorted(maps.Keys(files))
	result := make([]ResourceFileEntry, 0, len(paths))
	for _, filename := range paths {
		file := files[filename]
		result = append(result, ResourceFileEntry{Path: filename, ResourceFile: ResourceFile{Data: bytes.Clone(file.Data), Mode: file.Mode}})
	}
	return result
}

func regularMode(mode fs.FileMode) fs.FileMode {
	if mode.Perm() == 0 {
		return 0o644
	}
	return mode.Perm()
}

func safeRelativePath(filename string) (string, error) {
	filename = path.Clean(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "." || path.IsAbs(filename) || filename == ".." || strings.HasPrefix(filename, "../") {
		return "", errors.New("path escapes Skill root")
	}
	return filename, nil
}

func digestResourceFiles(files map[string]ResourceFile) string {
	hash := sha256.New()
	for _, filename := range slices.Sorted(maps.Keys(files)) {
		file := files[filename]
		contentHash := sha256.Sum256(file.Data)
		_, _ = fmt.Fprintf(hash, "%s\x00%04o\x00%d\x00%s\n", filename, regularMode(file.Mode).Perm(), len(file.Data), hex.EncodeToString(contentHash[:]))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func firstDiagnostic(diagnostics agentpackage.Diagnostics) string {
	if len(diagnostics) == 0 {
		return "unknown package error"
	}
	return diagnostics[0].Message
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func mcpTimeout(metadata map[string]any) int {
	value := metadata["call_timeout_seconds"]
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case float64:
		if number == math.Trunc(number) && number >= 0 && number <= 300 {
			return int(number)
		}
	case json.Number:
		if parsed, err := number.Int64(); err == nil && parsed >= 0 && parsed <= 300 {
			return int(parsed)
		}
	}
	return 0
}
