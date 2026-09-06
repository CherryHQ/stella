// Package agentpackage reads portable Agent Plugins packages.
//
// This package deliberately has no runtime, registry, or process-launching
// dependencies. It turns a directory package into data for a later integration
// layer, while keeping the standard's component failure boundaries intact.
package agentpackage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	PluginSchemaV1    = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	MCPV1Schema       = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
	StellaNamespace   = "com.cherryhq.stella"
	StellaExtensionV1 = "1"

	// These limits match the managed skill store's existing per-file and
	// manifest ceilings. The package reader only loads the fixed SKILL.md file.
	maxSkillBytes    = 32 << 20
	maxManifestBytes = 256 << 10
)

var pluginNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Diagnostic is a non-secret explanation of a package issue. Paths are
// package-relative whenever the issue belongs to a package file.
type Diagnostic struct {
	Severity Severity
	Code     string
	Path     string
	Message  string
}

type Diagnostics []Diagnostic

func (d Diagnostics) HasErrors() bool {
	for _, item := range d {
		if item.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (d *Diagnostics) add(severity Severity, code, path, format string, args ...any) {
	*d = append(*d, Diagnostic{Severity: severity, Code: code, Path: path, Message: fmt.Sprintf(format, args...)})
}

type Package struct {
	Root       string
	Manifest   Manifest
	Skills     []Skill
	MCPServers []MCPServer
	Extension  *StellaExtension
}

type Manifest struct {
	Schema      string
	Name        string
	Version     string
	Description string
	Author      *Author
	Homepage    string
	Repository  string
	License     string
	Keywords    []string
}

type Author struct {
	Name  string
	Email string
	URL   string
}

type Skill struct {
	Name        string
	Directory   string
	Path        string
	Content     []byte
	Mode        fs.FileMode
	Description string
}

type MCPServer struct {
	Name    string
	Type    string
	URL     string
	Headers map[string]string
}

type BinaryRequirement struct {
	Name    string                     `json:"name"`
	Tool    string                     `json:"tool"`
	Version string                     `json:"version"`
	Options map[string]json.RawMessage `json:"options"`
}

type SessionEnvRequirement struct {
	EnvVar   string `json:"env_var"`
	Source   string `json:"source"`
	Required bool   `json:"required"`
}

type OAuthRequirement struct {
	Provider string         `json:"provider"`
	Scopes   []string       `json:"scopes"`
	Bindings []OAuthBinding `json:"bindings"`
}

type OAuthBinding struct {
	Credential string `json:"credential"`
	EnvVar     string `json:"env_var"`
	Connection string `json:"connection"`
}

// StellaExtension contains declarations consumed by Stella. They describe
// requirements and presentation only. It has no field for executable code,
// native implementations, secrets, or scoped database state.
type StellaExtension struct {
	Version    string
	Binaries   []BinaryRequirement
	SessionEnv []SessionEnvRequirement
	OAuth      []OAuthRequirement
}

// Load reads a package using client-tolerant semantics. Fatal package issues
// leave Package nil. Component issues only remove the affected component.
func Load(root string) (*Package, Diagnostics) {
	return load(root, false)
}

// ValidateAuthoring validates a package as a Stella-authored package. Unlike
// Load, unknown manifest fields are errors and unsupported MCP transports are
// authoring failures. Unknown extension namespaces remain portable data and
// are intentionally ignored.
func ValidateAuthoring(root string) Diagnostics {
	_, diagnostics := load(root, true)
	return diagnostics
}

func load(root string, strict bool) (*Package, Diagnostics) {
	var diagnostics Diagnostics
	resolvedRoot, ok := resolveRoot(root, &diagnostics)
	if !ok {
		return nil, diagnostics
	}
	packageRoot, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		diagnostics.add(SeverityError, "package.root", "", "open package root: %v", err)
		return nil, diagnostics
	}
	defer func() { _ = packageRoot.Close() }()
	manifestInfo, err := packageRoot.Stat("plugin.json")
	if err != nil {
		code := "manifest.read"
		if linkInfo, linkErr := packageRoot.Lstat("plugin.json"); linkErr == nil && linkInfo.Mode()&fs.ModeSymlink != 0 {
			code = "path.escape"
		}
		diagnostics.add(SeverityError, code, "plugin.json", "manifest cannot be resolved within package root: %v", err)
		return nil, diagnostics
	}
	if !manifestInfo.Mode().IsRegular() {
		diagnostics.add(SeverityError, "manifest.read", "plugin.json", "manifest must be a regular file")
		return nil, diagnostics
	}
	manifestBytes, err := readLimited(packageRoot, "plugin.json", maxManifestBytes)
	if err != nil {
		diagnostics.add(SeverityError, "manifest.read", "plugin.json", "read manifest: %v", err)
		return nil, diagnostics
	}
	manifest, extensionRaw, fatal := parseManifest(manifestBytes, strict, &diagnostics)
	if fatal {
		return nil, diagnostics
	}

	pkg := &Package{Root: resolvedRoot, Manifest: manifest}
	if extensionRaw != nil {
		if raw, exists := extensionRaw[StellaNamespace]; exists {
			extension := parseStellaExtension(raw, strict, &diagnostics)
			if extension != nil {
				pkg.Extension = extension
			}
		}
	}

	loadSkills(packageRoot, pkg, strict, &diagnostics)
	loadMCP(packageRoot, resolvedRoot, manifest.Schema, pkg, strict, &diagnostics)
	return pkg, diagnostics
}

func resolveRoot(root string, diagnostics *Diagnostics) (string, bool) {
	if root == "" {
		diagnostics.add(SeverityError, "package.root", "", "package root is required")
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		diagnostics.add(SeverityError, "package.root", "", "resolve package root: %v", err)
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("path is not a directory")
		}
		diagnostics.add(SeverityError, "package.root", "", "package root: %v", err)
		return "", false
	}
	return filepath.Clean(resolved), true
}

func optionalPackagePath(root *os.Root, relative string, strict bool, diagnostics *Diagnostics, kind string) (bool, bool) {
	if _, err := root.Lstat(relative); errors.Is(err, fs.ErrNotExist) {
		return false, true
	} else if err != nil {
		diagnostics.add(componentSeverity(strict), "path.stat", filepath.ToSlash(relative), "stat %s path: %v", kind, err)
		return false, false
	}
	if _, err := root.Stat(relative); err != nil {
		diagnostics.add(componentSeverity(strict), "path.escape", filepath.ToSlash(relative), "%s path cannot be resolved within package root: %v", kind, err)
		return false, false
	}
	return true, true
}

func readLimited(root *os.Root, relative string, limit int64) ([]byte, error) {
	file, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d-byte limit", limit)
	}
	return data, nil
}

func parseManifest(data []byte, strict bool, diagnostics *Diagnostics) (Manifest, map[string]json.RawMessage, bool) {
	var values map[string]json.RawMessage
	if err := decodeObject(data, &values); err != nil {
		diagnostics.add(SeverityError, "manifest.invalid", "plugin.json", "manifest must be a JSON object: %v", err)
		return Manifest{}, nil, true
	}
	known := map[string]bool{"$schema": true, "name": true, "version": true, "description": true, "author": true, "homepage": true, "repository": true, "license": true, "keywords": true, "extensions": true}
	for key := range values {
		if !known[key] {
			severity := SeverityWarning
			if strict {
				severity = SeverityError
			}
			diagnostics.add(severity, "manifest.unknown_field", "plugin.json", "unknown manifest field %q is ignored", key)
		}
	}
	var manifest Manifest
	if err := unmarshalField(values, "$schema", &manifest.Schema); err != nil || manifest.Schema != PluginSchemaV1 {
		diagnostics.add(SeverityError, "manifest.schema", "plugin.json", "unsupported or missing $schema, want %q", PluginSchemaV1)
	}
	if err := unmarshalField(values, "name", &manifest.Name); err != nil || !validPluginName(manifest.Name) {
		diagnostics.add(SeverityError, "manifest.name", "plugin.json", "name must be 1-64 lowercase letters, digits, hyphens, or periods; no leading/trailing separator or repeated --/..")
	}
	for key, target := range map[string]any{"version": &manifest.Version, "description": &manifest.Description, "homepage": &manifest.Homepage, "repository": &manifest.Repository, "license": &manifest.License} {
		if raw, exists := values[key]; exists && (isJSONNull(raw) || unmarshalField(values, key, target) != nil) {
			diagnostics.add(SeverityError, "manifest.field", "plugin.json", "%s must be a string", key)
		}
	}
	if raw, exists := values["keywords"]; exists {
		if err := decodeStringArray(raw, &manifest.Keywords); err != nil {
			diagnostics.add(SeverityError, "manifest.field", "plugin.json", "keywords must be an array of strings")
		}
	}
	if raw, exists := values["author"]; exists {
		var authorValues map[string]json.RawMessage
		if decodeObject(raw, &authorValues) != nil {
			diagnostics.add(SeverityError, "manifest.author", "plugin.json", "author must be an object")
		} else {
			for key := range authorValues {
				if key != "name" && key != "email" && key != "url" {
					diagnostics.add(SeverityError, "manifest.author", "plugin.json", "unknown author field %q", key)
				}
			}
			author := &Author{}
			for key, target := range map[string]any{"name": &author.Name, "email": &author.Email, "url": &author.URL} {
				if raw, exists := authorValues[key]; exists && (isJSONNull(raw) || unmarshalField(authorValues, key, target) != nil) {
					diagnostics.add(SeverityError, "manifest.author", "plugin.json", "author.%s must be a string", key)
				}
			}
			manifest.Author = author
		}
	}
	var extensions map[string]json.RawMessage
	if raw, exists := values["extensions"]; exists {
		if decodeObject(raw, &extensions) != nil {
			diagnostics.add(componentSeverity(strict), "manifest.extensions", "plugin.json", "non-object extensions field is ignored")
		}
	}
	if diagnosticsForManifestFatal(*diagnostics) {
		return Manifest{}, extensions, true
	}
	return manifest, extensions, false
}

func diagnosticsForManifestFatal(diagnostics Diagnostics) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError && strings.HasPrefix(diagnostic.Code, "manifest.") {
			return true
		}
	}
	return false
}

func decodeObject(data []byte, target *map[string]json.RawMessage) error {
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	if *target == nil {
		return errors.New("expected object")
	}
	return nil
}

func unmarshalField(values map[string]json.RawMessage, key string, target any) error {
	raw, exists := values[key]
	if !exists {
		return errors.New("missing field")
	}
	return json.Unmarshal(raw, target)
}

func isJSONNull(data []byte) bool { return bytes.Equal(bytes.TrimSpace(data), []byte("null")) }

func decodeStringArray(data []byte, target *[]string) error {
	if isJSONNull(data) {
		return errors.New("expected an array")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil || values == nil {
		return errors.New("expected an array")
	}
	*target = make([]string, len(values))
	for i, raw := range values {
		if isJSONNull(raw) || json.Unmarshal(raw, &(*target)[i]) != nil {
			return fmt.Errorf("item %d must be a string", i)
		}
	}
	return nil
}

func decodeStringMap(data []byte, target *map[string]string) error {
	if isJSONNull(data) {
		return errors.New("expected an object")
	}
	var values map[string]json.RawMessage
	if err := decodeObject(data, &values); err != nil {
		return err
	}
	*target = make(map[string]string, len(values))
	for key, raw := range values {
		var value string
		if isJSONNull(raw) || json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("value for %q must be a string", key)
		}
		(*target)[key] = value
	}
	return nil
}

func validPluginName(name string) bool {
	return utf8.RuneCountInString(name) >= 1 && utf8.RuneCountInString(name) <= 64 && pluginNamePattern.MatchString(name) && !strings.Contains(name, "--") && !strings.Contains(name, "..")
}

func loadSkills(root *os.Root, pkg *Package, strict bool, diagnostics *Diagnostics) {
	exists, ok := optionalPackagePath(root, "skills", strict, diagnostics, "skills")
	if !exists || !ok {
		return
	}
	info, err := root.Stat("skills")
	if err != nil || !info.IsDir() {
		diagnostics.add(componentSeverity(strict), "skills.location", "skills", "skills location is not a directory; skills are disabled")
		return
	}
	entries, err := fs.ReadDir(root.FS(), "skills")
	if err != nil {
		diagnostics.add(componentSeverity(strict), "skills.read", "skills", "read skills directory: %v", err)
		return
	}
	for _, entry := range entries {
		entryPath := filepath.Join("skills", entry.Name())
		entryInfo, entryErr := root.Stat(entryPath)
		if entryErr != nil {
			diagnostics.add(componentSeverity(strict), "path.escape", filepath.ToSlash(entryPath), "skill directory cannot be resolved within package root: %v; skill skipped", entryErr)
			continue
		}
		if !entryInfo.IsDir() {
			continue
		}
		children, childErr := fs.ReadDir(root.FS(), entryPath)
		if childErr != nil {
			diagnostics.add(componentSeverity(strict), "skill.read", filepath.ToSlash(entryPath), "read skill directory: %v; skills skipped", childErr)
			continue
		}
		hasSkillFile := false
		for _, child := range children {
			if child.Name() == "SKILL.md" {
				hasSkillFile = true
				break
			}
		}
		if !hasSkillFile {
			continue
		}
		relative := filepath.ToSlash(filepath.Join("skills", entry.Name(), "SKILL.md"))
		info, err := root.Stat(filepath.FromSlash(relative))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			diagnostics.add(componentSeverity(strict), "path.escape", relative, "skill path cannot be resolved within package root: %v; skill skipped", err)
			continue
		}
		if !info.Mode().IsRegular() {
			diagnostics.add(componentSeverity(strict), "skill.file", filepath.ToSlash(relative), "SKILL.md is not a regular file; skill skipped")
			continue
		}
		data, err := readLimited(root, filepath.FromSlash(relative), maxSkillBytes)
		if err != nil {
			diagnostics.add(componentSeverity(strict), "skill.read", filepath.ToSlash(relative), "read skill: %v; skill skipped", err)
			continue
		}
		frontmatter, err := parseSkillFrontmatter(data)
		if err != nil {
			diagnostics.add(componentSeverity(strict), "skill.invalid", filepath.ToSlash(relative), "%v; skill skipped", err)
			continue
		}
		if frontmatter.Name != entry.Name() || !validSkillName(frontmatter.Name) || frontmatter.Description == "" || len([]rune(frontmatter.Description)) > 1024 {
			diagnostics.add(componentSeverity(strict), "skill.metadata", filepath.ToSlash(relative), "skill name must match its directory and use the Agent Skills name rules; description must be 1-1024 characters; skill skipped")
			continue
		}
		pkg.Skills = append(pkg.Skills, Skill{Name: frontmatter.Name, Directory: filepath.ToSlash(filepath.Join("skills", entry.Name())), Path: filepath.ToSlash(relative), Content: bytes.Clone(data), Mode: info.Mode(), Description: frontmatter.Description})
	}
	slices.SortFunc(pkg.Skills, func(left, right Skill) int { return strings.Compare(left.Name, right.Name) })
}

func componentSeverity(strict bool) Severity {
	if strict {
		return SeverityError
	}
	return SeverityWarning
}

type skillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"`
}

func parseSkillFrontmatter(data []byte) (skillFrontmatter, error) {
	var frontmatter skillFrontmatter
	if !bytes.HasPrefix(data, []byte("---")) {
		return frontmatter, errors.New("SKILL.md must start with YAML frontmatter")
	}
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) < 3 || string(bytes.TrimSpace(lines[0])) != "---" {
		return frontmatter, errors.New("SKILL.md has invalid frontmatter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if string(bytes.TrimSpace(lines[i])) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return frontmatter, errors.New("SKILL.md frontmatter is not closed")
	}
	frontmatterData := bytes.Join(lines[1:end], []byte("\n"))
	decoder := yaml.NewDecoder(bytes.NewReader(frontmatterData))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return frontmatter, fmt.Errorf("invalid Agent Skills frontmatter: %w", err)
	}
	if err := validateSkillFrontmatterNode(&document); err != nil {
		return frontmatter, err
	}
	if err := yaml.Unmarshal(frontmatterData, &frontmatter); err != nil {
		return frontmatter, fmt.Errorf("invalid Agent Skills frontmatter: %w", err)
	}
	if frontmatter.Metadata == nil {
		frontmatter.Metadata = map[string]string{}
	}
	if frontmatter.Compatibility != "" && len([]rune(frontmatter.Compatibility)) > 500 {
		return frontmatter, errors.New("compatibility exceeds 500 characters")
	}
	return frontmatter, nil
}

func validSkillName(name string) bool {
	runes := []rune(name)
	if len(runes) < 1 || len(runes) > 64 || runes[0] == '-' || runes[len(runes)-1] == '-' || strings.Contains(name, "--") {
		return false
	}
	for _, char := range runes {
		if char != '-' && !unicode.IsDigit(char) && (!unicode.IsLetter(char) || !unicode.IsLower(char)) {
			return false
		}
	}
	return true
}

func validateSkillFrontmatterNode(document *yaml.Node) error {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("agent skills frontmatter must be a mapping")
	}
	allowed := map[string]bool{"name": true, "description": true, "license": true, "compatibility": true, "metadata": true, "allowed-tools": true}
	mapping := document.Content[0]
	for index := 0; index < len(mapping.Content); index += 2 {
		key, value := mapping.Content[index], mapping.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || !allowed[key.Value] {
			return fmt.Errorf("unknown or invalid Agent Skills frontmatter field %q", key.Value)
		}
		if key.Value == "metadata" {
			if value.Kind != yaml.MappingNode {
				return errors.New("metadata must be a string map")
			}
			for child := 0; child < len(value.Content); child += 2 {
				metaKey, metaValue := value.Content[child], value.Content[child+1]
				if metaKey.Kind != yaml.ScalarNode || metaKey.Tag != "!!str" || metaValue.Kind != yaml.ScalarNode || metaValue.Tag != "!!str" {
					return errors.New("metadata keys and values must be strings")
				}
			}
			continue
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return fmt.Errorf("%s must be a string", key.Value)
		}
		if key.Value == "compatibility" && value.Value == "" {
			return errors.New("compatibility must be 1-500 characters when present")
		}
	}
	return nil
}

func loadMCP(root *os.Root, rootPath, manifestSchema string, pkg *Package, strict bool, diagnostics *Diagnostics) {
	exists, ok := optionalPackagePath(root, "mcp.json", strict, diagnostics, "MCP")
	if !exists || !ok {
		return
	}
	info, err := root.Stat("mcp.json")
	if err != nil || !info.Mode().IsRegular() {
		diagnostics.add(componentSeverity(strict), "mcp.location", "mcp.json", "mcp.json is not a regular file; MCP is disabled")
		return
	}
	data, err := readLimited(root, "mcp.json", maxManifestBytes)
	if err != nil {
		diagnostics.add(componentSeverity(strict), "mcp.read", "mcp.json", "read MCP configuration: %v; MCP is disabled", err)
		return
	}
	var values map[string]json.RawMessage
	if err := decodeObject(data, &values); err != nil {
		diagnostics.add(componentSeverity(strict), "mcp.invalid", "mcp.json", "MCP configuration must be a JSON object: %v; MCP is disabled", err)
		return
	}
	for key := range values {
		if key != "$schema" && key != "mcpServers" {
			diagnostics.add(componentSeverity(strict), "mcp.unknown_field", "mcp.json", "unknown MCP field %q; MCP is disabled", key)
			return
		}
	}
	var schema string
	if err := unmarshalField(values, "$schema", &schema); err != nil || schema != MCPV1Schema || manifestSchema != PluginSchemaV1 {
		diagnostics.add(componentSeverity(strict), "mcp.schema", "mcp.json", "MCP schema is missing or unsupported; MCP is disabled")
		return
	}
	var servers map[string]json.RawMessage
	if err := unmarshalField(values, "mcpServers", &servers); err != nil || servers == nil {
		diagnostics.add(componentSeverity(strict), "mcp.servers", "mcp.json", "mcpServers must be an object; MCP is disabled")
		return
	}
	serverNames := make([]string, 0, len(servers))
	for name := range servers {
		serverNames = append(serverNames, name)
	}
	slices.Sort(serverNames)
	for _, name := range serverNames {
		server, serverOK := parseMCPServer(name, servers[name], root, rootPath, strict, diagnostics)
		if serverOK {
			pkg.MCPServers = append(pkg.MCPServers, server)
		}
	}
}

func parseMCPServer(name string, data json.RawMessage, packageRoot *os.Root, rootPath string, strict bool, diagnostics *Diagnostics) (MCPServer, bool) {
	var values map[string]json.RawMessage
	if err := decodeObject(data, &values); err != nil {
		diagnostics.add(componentSeverity(strict), "mcp.server.invalid", "mcp.json", "server %q is not an object; skipped", name)
		return MCPServer{}, false
	}
	var transport string
	if err := unmarshalField(values, "type", &transport); err != nil {
		diagnostics.add(componentSeverity(strict), "mcp.server.type", "mcp.json", "server %q has no valid type; skipped", name)
		return MCPServer{}, false
	}
	allowed := map[string]bool{"type": true}
	server := MCPServer{Name: name, Type: transport}
	switch transport {
	case "streamable-http", "sse":
		allowed["url"], allowed["headers"] = true, true
		if err := unmarshalField(values, "url", &server.URL); err != nil || !validRemoteURL(server.URL) {
			diagnostics.add(componentSeverity(strict), "mcp.server.url", "mcp.json", "server %q has an invalid URL; skipped", name)
			return MCPServer{}, false
		}
		if raw, exists := values["headers"]; exists {
			if err := decodeStringMap(raw, &server.Headers); err != nil || !validHeaders(server.Headers) {
				diagnostics.add(componentSeverity(strict), "mcp.server.headers", "mcp.json", "server %q has invalid headers; skipped", name)
				return MCPServer{}, false
			}
		}
	case "stdio":
		allowed["command"], allowed["args"], allowed["env"], allowed["cwd"] = true, true, true, true
		for key := range values {
			if !allowed[key] {
				diagnostics.add(componentSeverity(strict), "mcp.server.field", "mcp.json", "server %q has unknown or cross-transport field %q; skipped", name, key)
				return MCPServer{}, false
			}
		}
		var command string
		if err := unmarshalField(values, "command", &command); err != nil || command == "" || strings.ContainsAny(command, "\t\r\n ") {
			diagnostics.add(componentSeverity(strict), "mcp.server.command", "mcp.json", "server %q has an invalid stdio command; skipped", name)
			return MCPServer{}, false
		}
		if strings.HasPrefix(command, "./") && !validPluginRelativePath(command, packageRoot) {
			diagnostics.add(componentSeverity(strict), "mcp.server.command", "mcp.json", "server %q has a plugin-relative command outside the package root; skipped", name)
			return MCPServer{}, false
		}
		var args []string
		if raw, exists := values["args"]; exists && (isJSONNull(raw) || json.Unmarshal(raw, &args) != nil || args == nil) {
			diagnostics.add(componentSeverity(strict), "mcp.server.args", "mcp.json", "server %q has invalid stdio args; skipped", name)
			return MCPServer{}, false
		}
		var env map[string]string
		if raw, exists := values["env"]; exists && (decodeStringMap(raw, &env) != nil || hasReservedEnv(env)) {
			diagnostics.add(componentSeverity(strict), "mcp.server.env", "mcp.json", "server %q has invalid stdio env; skipped", name)
			return MCPServer{}, false
		}
		var cwd string
		if raw, exists := values["cwd"]; exists {
			if isJSONNull(raw) || json.Unmarshal(raw, &cwd) != nil || !validStdioCWD(cwd, packageRoot, rootPath) {
				diagnostics.add(componentSeverity(strict), "mcp.server.cwd", "mcp.json", "server %q has an invalid stdio cwd; skipped", name)
				return MCPServer{}, false
			}
		}
		diagnostics.add(componentSeverity(strict), "mcp.server.unsupported_transport", "mcp.json", "server %q uses stdio; Stella recognizes it but does not launch package processes", name)
		return MCPServer{}, false
	default:
		diagnostics.add(componentSeverity(strict), "mcp.server.transport", "mcp.json", "server %q uses unsupported transport %q; skipped", name, transport)
		return MCPServer{}, false
	}
	for key := range values {
		if !allowed[key] {
			diagnostics.add(componentSeverity(strict), "mcp.server.field", "mcp.json", "server %q has unknown or cross-transport field %q; skipped", name, key)
			return MCPServer{}, false
		}
	}
	return server, true
}

func validRemoteURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validHeaders(headers map[string]string) bool {
	seen := make(map[string]struct{}, len(headers))
	for name, value := range headers {
		canonical := strings.ToLower(name)
		if _, exists := seen[canonical]; exists || !validHeaderField(name) || !validHeaderValue(value) || credentialHeader(canonical) {
			return false
		}
		seen[canonical] = struct{}{}
	}
	return true
}

func credentialHeader(name string) bool {
	switch name {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key":
		return true
	default:
		return false
	}
}

func validStdioCWD(cwd string, packageRoot *os.Root, rootPath string) bool {
	if cwd == "${PLUGIN_DATA}" || strings.HasPrefix(cwd, "${PLUGIN_DATA}/") {
		return true
	}
	if cwd == "${PLUGIN_ROOT}" {
		return true
	}
	if after, ok := strings.CutPrefix(cwd, "${PLUGIN_ROOT}/"); ok {
		cwd = "./" + after
	}
	if !strings.HasPrefix(cwd, "./") {
		return false
	}
	relative := filepath.FromSlash(cwd[2:])
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relativeEscapesRoot(relative) {
		return false
	}
	if _, err := packageRoot.Stat(relative); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return withinRootLexical(rootPath, filepath.Join(rootPath, relative))
}

func validPluginRelativePath(value string, packageRoot *os.Root) bool {
	if !strings.HasPrefix(value, "./") {
		return true
	}
	relative := filepath.FromSlash(value[2:])
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relativeEscapesRoot(relative) {
		return false
	}
	if _, err := packageRoot.Stat(relative); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return true
}

func relativeEscapesRoot(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func hasReservedEnv(values map[string]string) bool {
	for key := range values {
		if strings.EqualFold(key, "PLUGIN_ROOT") || strings.EqualFold(key, "PLUGIN_DATA") {
			return true
		}
	}
	return false
}

func validHeaderField(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char <= 0x20 || char >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", char) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for _, char := range value {
		if (char < 0x20 && char != '\t') || char == 0x7f {
			return false
		}
	}
	return true
}

func withinRootLexical(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func parseStellaExtension(data json.RawMessage, strict bool, diagnostics *Diagnostics) *StellaExtension {
	var values map[string]json.RawMessage
	if err := decodeObject(data, &values); err != nil {
		diagnostics.add(componentSeverity(strict), "extension.invalid", "plugin.json", "Stella extension must be an object and is ignored")
		return nil
	}
	known := map[string]bool{"version": true, "binaries": true, "session_env": true, "oauth": true}
	for key := range values {
		if !known[key] {
			severity := SeverityWarning
			if strict {
				severity = SeverityError
			}
			diagnostics.add(severity, "extension.field", "plugin.json", "unknown Stella extension field %q", key)
		}
	}
	var extension StellaExtension
	if err := unmarshalField(values, "version", &extension.Version); err != nil || extension.Version != StellaExtensionV1 {
		diagnostics.add(componentSeverity(strict), "extension.version", "plugin.json", "Stella extension version is unsupported or missing; want %q", StellaExtensionV1)
		return nil
	}
	if raw, exists := values["binaries"]; exists && (validateArrayObjects(raw, map[string]bool{"name": true, "tool": true, "version": true, "options": true}) != nil || json.Unmarshal(raw, &extension.Binaries) != nil) {
		diagnostics.add(componentSeverity(strict), "extension.binaries", "plugin.json", "invalid Stella binaries declaration; extension is ignored")
		return nil
	}
	if raw, exists := values["session_env"]; exists && (validateArrayObjects(raw, map[string]bool{"env_var": true, "source": true, "required": true}) != nil || json.Unmarshal(raw, &extension.SessionEnv) != nil) {
		diagnostics.add(componentSeverity(strict), "extension.session_env", "plugin.json", "invalid Stella session_env declaration; extension is ignored")
		return nil
	}
	if raw, exists := values["oauth"]; exists {
		if err := validateOAuthRaw(raw); err != nil {
			diagnostics.add(componentSeverity(strict), "extension.oauth", "plugin.json", "invalid Stella OAuth declaration: %v; extension is ignored", err)
			return nil
		}
		parsed, err := parseOAuthRequirements(raw)
		if err != nil {
			diagnostics.add(componentSeverity(strict), "extension.oauth", "plugin.json", "invalid Stella OAuth declaration: %v; extension is ignored", err)
			return nil
		}
		extension.OAuth = parsed
	}
	if containsSecretField(values) {
		diagnostics.add(componentSeverity(strict), "extension.secret", "plugin.json", "Stella extension contains a secret field; extension is ignored")
		return nil
	}
	if err := validateStellaExtension(extension); err != nil {
		diagnostics.add(componentSeverity(strict), "extension.invalid", "plugin.json", "%v; extension is ignored", err)
		return nil
	}
	if strict && diagnosticsForExtensionFatal(*diagnostics) {
		return nil
	}
	return &extension
}

func parseOAuthRequirements(data json.RawMessage) ([]OAuthRequirement, error) {
	var requirements []OAuthRequirement
	if isJSONNull(data) || json.Unmarshal(data, &requirements) != nil || requirements == nil {
		return nil, errors.New("OAuth declaration must be an array")
	}
	return requirements, nil
}

func validateArrayObjects(data json.RawMessage, allowed map[string]bool) error {
	if isJSONNull(data) {
		return errors.New("expected an array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil || entries == nil {
		return errors.New("expected an array")
	}
	for _, entry := range entries {
		var object map[string]json.RawMessage
		if err := decodeObject(entry, &object); err != nil {
			return err
		}
		for key := range object {
			if !allowed[key] {
				return fmt.Errorf("unknown field %q", key)
			}
			if isJSONNull(object[key]) {
				return fmt.Errorf("field %q must not be null", key)
			}
			if key == "options" {
				var options map[string]json.RawMessage
				if err := decodeObject(object[key], &options); err != nil {
					return errors.New("options must be an object")
				}
			}
		}
	}
	return nil
}

func validateOAuthRaw(data json.RawMessage) error {
	if isJSONNull(data) {
		return errors.New("expected an array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil || entries == nil {
		return errors.New("expected an array")
	}
	for _, entry := range entries {
		var object map[string]json.RawMessage
		if err := decodeObject(entry, &object); err != nil {
			return err
		}
		for key := range object {
			if key != "provider" && key != "scopes" && key != "bindings" {
				return fmt.Errorf("unknown field %q", key)
			}
			if isJSONNull(object[key]) {
				return fmt.Errorf("field %q must not be null", key)
			}
		}
		if raw, exists := object["scopes"]; exists {
			var scopes []string
			if err := decodeStringArray(raw, &scopes); err != nil {
				return err
			}
		}
		if raw, exists := object["bindings"]; exists {
			if err := validateArrayObjects(raw, map[string]bool{"credential": true, "env_var": true, "connection": true}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStellaExtension(extension StellaExtension) error {
	for i, binary := range extension.Binaries {
		if binary.Name == "" || binary.Tool == "" {
			return fmt.Errorf("binary[%d] requires name and tool", i)
		}
	}
	for i, env := range extension.SessionEnv {
		if env.EnvVar == "" || env.Source == "" {
			return fmt.Errorf("session_env[%d] requires env_var and source", i)
		}
	}
	for i, oauth := range extension.OAuth {
		if oauth.Provider == "" {
			return fmt.Errorf("oauth[%d] requires provider", i)
		}
		for j, binding := range oauth.Bindings {
			if binding.Credential == "" || (binding.EnvVar == "" && binding.Connection == "") {
				return fmt.Errorf("oauth[%d].bindings[%d] requires credential and env_var or connection", i, j)
			}
		}
	}
	return nil
}

func diagnosticsForExtensionFatal(diagnostics Diagnostics) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError && strings.HasPrefix(diagnostic.Code, "extension.") {
			return true
		}
	}
	return false
}

func containsSecretField(values map[string]json.RawMessage) bool {
	for key := range values {
		if secretFieldName(key) {
			return true
		}
		var value any
		if json.Unmarshal(values[key], &value) == nil && containsSecretValue(value) {
			return true
		}
	}
	return false
}

func containsSecretValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if secretFieldName(key) || containsSecretValue(child) {
				return true
			}
		}
	case []any:
		if slices.ContainsFunc(typed, containsSecretValue) {
			return true
		}
	}
	return false
}

func secretFieldName(key string) bool {
	switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", "")) {
	case "secret", "clientsecret", "token", "password", "apikey", "accesskey", "privatekey":
		return true
	default:
		return false
	}
}
