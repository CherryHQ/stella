package delegate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/resources"
)

const maxRuntimeDelegateBytes int64 = 256 << 10

// RuntimePresetLoadConfig describes only runner-visible preset tiers. All roots
// are canonical sandbox coordinates; host paths are deliberately not accepted.
type RuntimePresetLoadConfig struct {
	HasPrincipal bool
	ProjectRoot  string // empty when no project is configured; otherwise under /workspace
}

// LoadRuntimeDelegatePresets discovers presets from the supplied runner
// filesystem and embedded registry. It borrows both dependencies and never
// closes the filesystem. Later tiers replace duplicate names in their original
// position, preserving deterministic command presentation.
func LoadRuntimeDelegatePresets(ctx context.Context, filesystem sandbox.Filesystem, registry *resources.Registry, cfg RuntimePresetLoadConfig) ([]DelegatePreset, error) {
	if filesystem == nil {
		return nil, errors.New("delegate: filesystem is required")
	}
	if registry == nil {
		return nil, errors.New("delegate: builtin resource registry is required")
	}
	if cfg.ProjectRoot != "" && (!sandbox.IsCanonicalFilesystemPath(cfg.ProjectRoot) || (cfg.ProjectRoot != sandbox.PathWorkspace && !strings.HasPrefix(cfg.ProjectRoot, sandbox.PathWorkspace+"/"))) {
		return nil, fmt.Errorf("delegate: project root %q is not under /workspace", cfg.ProjectRoot)
	}

	index := map[string]int{}
	var presets []DelegatePreset
	add := func(p DelegatePreset) {
		if i, ok := index[p.Name]; ok {
			presets[i] = p
			return
		}
		index[p.Name] = len(presets)
		presets = append(presets, p)
	}
	for _, resource := range registry.List(resources.KindDelegate) {
		preset, err := runtimeBuiltinPreset(resource)
		if err != nil {
			return nil, err
		}
		add(preset)
	}

	seen := map[string]struct{}{}
	loadTier := func(root, source string) error {
		dir := path.Join(root, ".agents", "delegates")
		if _, ok := seen[dir]; ok {
			return nil
		}
		seen[dir] = struct{}{}
		loaded, err := loadRuntimePresetsFromDir(ctx, filesystem, dir, source)
		if err != nil {
			return err
		}
		for _, preset := range loaded {
			add(preset)
		}
		return nil
	}
	if cfg.HasPrincipal {
		if err := loadTier(sandbox.PathUser, "user"); err != nil {
			return nil, err
		}
	}
	if err := loadTier(sandbox.PathWorkspace, "agent"); err != nil {
		return nil, err
	}
	if cfg.ProjectRoot != "" {
		if err := loadTier(cfg.ProjectRoot, "project"); err != nil {
			return nil, err
		}
	}
	return presets, nil
}

func runtimeBuiltinPreset(resource resources.Resource) (DelegatePreset, error) {
	if resource.ID == "" || resource.Name == "" || strings.TrimSpace(resource.Description) == "" {
		return DelegatePreset{}, fmt.Errorf("delegate: invalid builtin preset %q", resource.ID)
	}
	tools, hasTools, err := runtimeToolsMetadata(resource.Metadata)
	if err != nil {
		return DelegatePreset{}, fmt.Errorf("delegate: invalid builtin preset %q: %w", resource.ID, err)
	}
	var timeout time.Duration
	if raw, ok := resource.Metadata["timeout"]; ok {
		s, ok := raw.(string)
		if !ok {
			return DelegatePreset{}, fmt.Errorf("delegate: invalid builtin preset %q: timeout must be a string", resource.ID)
		}
		timeout, err = time.ParseDuration(s)
		if err != nil {
			return DelegatePreset{}, fmt.Errorf("delegate: invalid builtin preset %q: invalid timeout: %w", resource.ID, err)
		}
	}
	model := ""
	if raw, ok := resource.Metadata["model"]; ok {
		var valid bool
		model, valid = raw.(string)
		if !valid {
			return DelegatePreset{}, fmt.Errorf("delegate: invalid builtin preset %q: model must be a string", resource.ID)
		}
	}
	return DelegatePreset{Name: resource.Name, Description: resource.Description, System: strings.TrimSpace(resource.Content), Tools: tools, HasTools: hasTools, Timeout: timeout, Model: model, FilePath: "builtin:" + resource.ID, Source: "builtin"}, nil
}

func runtimeToolsMetadata(metadata map[string]any) ([]string, bool, error) {
	raw, present := metadata["tools"]
	if !present {
		return nil, false, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false, errors.New("tools must be a list")
	}
	tools := make([]string, len(items))
	for i, item := range items {
		var valid bool
		tools[i], valid = item.(string)
		if !valid {
			return nil, false, errors.New("tools must contain strings")
		}
	}
	return tools, true, nil
}

func loadRuntimePresetsFromDir(ctx context.Context, filesystem sandbox.Filesystem, dir, source string) ([]DelegatePreset, error) {
	entries, err := filesystem.List(ctx, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("delegate: list %s: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	presets := make([]DelegatePreset, 0, len(entries))
	for _, entry := range entries {
		if err := validRuntimePresetEntry(entry); err != nil {
			return nil, err
		}
		if entry.IsDir || strings.HasPrefix(entry.Name, ".") || !strings.HasSuffix(entry.Name, ".md") {
			continue
		}
		filePath := path.Join(dir, entry.Name)
		preset, ok, err := loadRuntimePresetFile(ctx, filesystem, filePath, source)
		if err != nil {
			return nil, err
		}
		if ok {
			presets = append(presets, preset)
		}
	}
	return presets, nil
}

func validRuntimePresetEntry(entry sandbox.DirEntry) error {
	if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\\\x00") {
		return fmt.Errorf("delegate: malformed directory entry %q", entry.Name)
	}
	if entry.IsDir != entry.Mode.IsDir() || entry.Mode&fs.ModeSymlink != 0 || (!entry.IsDir && !entry.Mode.IsRegular()) {
		return fmt.Errorf("delegate: unsafe directory entry %q", entry.Name)
	}
	return nil
}

func loadRuntimePresetFile(ctx context.Context, filesystem sandbox.Filesystem, filePath, source string) (preset DelegatePreset, ok bool, err error) {
	reader, info, err := filesystem.Read(ctx, filePath, sandbox.ReadOptions{MaxBytes: maxRuntimeDelegateBytes})
	if err != nil {
		return DelegatePreset{}, false, fmt.Errorf("delegate: read %s: %w", filePath, err)
	}
	if reader == nil {
		return DelegatePreset{}, false, fmt.Errorf("delegate: read %s: nil reader", filePath)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return DelegatePreset{}, false, fmt.Errorf("delegate: read %s: %w", filePath, errors.Join(readErr, closeErr))
	}
	if info.Mode&fs.ModeSymlink != 0 || !info.Mode.IsRegular() || info.IsDir {
		return DelegatePreset{}, false, fmt.Errorf("delegate: unsafe preset file %s", filePath)
	}
	if int64(len(data)) > maxRuntimeDelegateBytes {
		return DelegatePreset{}, false, sandbox.ErrReadLimit
	}
	if int64(len(data)) != info.Size {
		return DelegatePreset{}, false, fmt.Errorf("delegate: preset file changed during read: %s", filePath)
	}
	parsed, parsedOK := parseRuntimePreset(string(data), filePath, source)
	return parsed, parsedOK, nil
}

func parseRuntimePreset(content, filePath, source string) (DelegatePreset, bool) {
	fm, body, err := parseDelegateFrontmatter(content)
	if err != nil {
		slog.Debug("failed to parse delegate preset frontmatter", "path", filePath, "error", err)
		return DelegatePreset{}, false
	}
	name := fm.Name
	if name == "" {
		name = strings.TrimSuffix(path.Base(filePath), ".md")
	}
	if strings.TrimSpace(fm.Description) == "" {
		slog.Debug("delegate preset missing description", "path", filePath)
		return DelegatePreset{}, false
	}
	var timeout time.Duration
	if fm.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(fm.Timeout)
		if err != nil {
			slog.Debug("invalid timeout in delegate preset", "path", filePath, "timeout", fm.Timeout, "error", err)
			return DelegatePreset{}, false
		}
	}
	return DelegatePreset{Name: name, Description: fm.Description, System: strings.TrimSpace(body), Tools: fm.Tools, HasTools: fm.HasTools, Timeout: timeout, Model: fm.Model, FilePath: filePath, Source: source}, true
}
