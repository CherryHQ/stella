package sandbox

import (
	"fmt"
	"os"
	"strings"
)

// Agent-facing filesystem environment variable names. Backends render their
// own path view for these variables, while their ownership stays invariant.
const (
	EnvHome            = "HOME"
	EnvStellaUserDir   = "STELLA_USER_DIR"
	EnvStellaAssetsDir = "STELLA_ASSETS_DIR"
	EnvTempDir         = "TMPDIR"
	EnvXDGConfigHome   = "XDG_CONFIG_HOME"
	EnvXDGDataHome     = "XDG_DATA_HOME"
	EnvXDGStateHome    = "XDG_STATE_HOME"
	EnvXDGCacheHome    = "XDG_CACHE_HOME"
	EnvXDGRuntimeDir   = "XDG_RUNTIME_DIR"
)

// FilesystemView is the agent-visible filesystem path view a backend exposes.
// Its roots are canonical backend views: POSIX sandbox paths or native host
// paths. UserDir is optional; when present, STELLA_ASSETS_DIR is derived as
// UserDir/assets. Without UserDir, persistent XDG directories live under Home.
// Home and TempDir are required.
type FilesystemView struct {
	Home    string
	UserDir string
	TempDir string
}

// ApplyFilesystemEnv applies the agent-facing filesystem contract to env.
// It preserves unrelated variables, clears unavailable optional roots and
// XDG_RUNTIME_DIR, and places persistent XDG directories under UserDir or Home.
func ApplyFilesystemEnv(env map[string]string, view FilesystemView) error {
	if env == nil {
		return fmt.Errorf("sandbox: filesystem environment map is required")
	}
	if view.Home == "" {
		return fmt.Errorf("sandbox: filesystem view requires %s", EnvHome)
	}
	if view.TempDir == "" {
		return fmt.Errorf("sandbox: filesystem view requires %s", EnvTempDir)
	}

	env[EnvHome] = view.Home
	env[EnvTempDir] = view.TempDir
	setOptionalEnv(env, EnvStellaUserDir, view.UserDir)
	assetsDir := ""
	if view.UserDir != "" {
		assetsDir = joinFilesystemRoot(view.UserDir, "assets")
	}
	setOptionalEnv(env, EnvStellaAssetsDir, assetsDir)

	persistentRoot := view.UserDir
	if persistentRoot == "" {
		persistentRoot = view.Home
	}
	env[EnvXDGConfigHome] = joinFilesystemRoot(persistentRoot, ".config")
	env[EnvXDGDataHome] = joinFilesystemRoot(persistentRoot, ".local/share")
	env[EnvXDGStateHome] = joinFilesystemRoot(persistentRoot, ".local/state")
	env[EnvXDGCacheHome] = joinFilesystemRoot(persistentRoot, ".cache")
	delete(env, EnvXDGRuntimeDir)
	return nil
}

// ExpandPathVariables expands one allowlisted variable only when it appears at
// the beginning of path. Values come exclusively from env, never the host
// process environment. Other paths are returned unchanged.
func ExpandPathVariables(path string, env map[string]string) (string, error) {
	name, suffix, hasVariable, err := SplitLeadingPathVariable(path)
	if err != nil {
		return "", err
	}
	if !hasVariable {
		return path, nil
	}
	if !isAllowedPathEnv(name) {
		return "", fmt.Errorf("sandbox: unsupported leading path variable; use %s", allowedPathEnvHint())
	}
	value := env[name]
	if value == "" {
		return "", fmt.Errorf("sandbox: leading path variable %s is unavailable in this sandbox", "$"+name)
	}
	return value + suffix, nil
}

func setOptionalEnv(env map[string]string, name, value string) {
	if value == "" {
		delete(env, name)
		return
	}
	env[name] = value
}

// joinFilesystemRoot derives a child in the root's path style. Roots are
// backend-supplied canonical paths, so this deliberately does not clean them.
func joinFilesystemRoot(root, suffix string) string {
	separator := "/"
	if strings.Contains(root, `\`) && !strings.Contains(root, "/") {
		separator = `\`
		suffix = strings.ReplaceAll(suffix, "/", separator)
	}

	root = trimTrailingFilesystemSeparators(root, separator)
	if root == "" || strings.HasSuffix(root, separator) {
		return root + suffix
	}
	return root + separator + suffix
}

func trimTrailingFilesystemSeparators(root, separator string) string {
	minimumLength := 0
	switch {
	case strings.HasPrefix(root, "/"):
		minimumLength = 1 // Preserve the POSIX volume root.
	case len(root) >= 3 && root[1] == ':' && root[2] == separator[0]:
		minimumLength = 3 // Preserve a Windows volume root.
	case separator == `\` && strings.HasPrefix(root, `\\`):
		minimumLength = 2 // Preserve the UNC prefix.
	case separator == `\` && strings.HasPrefix(root, `\`):
		minimumLength = 1 // Preserve the current-drive root.
	}
	for len(root) > minimumLength && root[len(root)-1] == separator[0] {
		root = root[:len(root)-1]
	}
	return root
}

// SplitLeadingPathVariable parses the restricted path-variable grammar shared
// by agent-facing file paths. It accepts exactly one leading $NAME or ${NAME},
// optionally followed by a path separator and suffix; it does not expand or
// allowlist the variable.
func SplitLeadingPathVariable(path string) (name, suffix string, hasVariable bool, err error) {
	if !strings.HasPrefix(path, "$") {
		return "", "", false, nil
	}
	if len(path) == 1 {
		return "", "", false, malformedPathVariableError()
	}
	if path[1] == '{' {
		end := strings.IndexByte(path[2:], '}')
		if end < 0 {
			return "", "", false, malformedPathVariableError()
		}
		end += 2
		name = path[2:end]
		suffix = path[end+1:]
	} else {
		end := 1
		for end < len(path) && isPathVariableChar(path[end], end == 1) {
			end++
		}
		name = path[1:end]
		suffix = path[end:]
	}
	if name == "" || !isPathVariableName(name) {
		return "", "", false, malformedPathVariableError()
	}
	if suffix != "" && !os.IsPathSeparator(suffix[0]) {
		return "", "", false, fmt.Errorf("sandbox: leading path variable must be the whole path or followed by a path separator")
	}
	return name, suffix, true, nil
}

func isPathVariableName(name string) bool {
	for i := range len(name) {
		if !isPathVariableChar(name[i], i == 0) {
			return false
		}
	}
	return true
}

func isPathVariableChar(c byte, first bool) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || !first && c >= '0' && c <= '9'
}

func malformedPathVariableError() error {
	return fmt.Errorf("sandbox: malformed leading path variable; use %s", allowedPathEnvHint())
}

func isAllowedPathEnv(name string) bool {
	switch name {
	case EnvHome, EnvStellaUserDir, EnvStellaAssetsDir, EnvTempDir:
		return true
	default:
		return false
	}
}

func allowedPathEnvHint() string {
	return "$HOME, $STELLA_USER_DIR, $STELLA_ASSETS_DIR, or $TMPDIR"
}
