package tools

import "path/filepath"

// StringArg returns the first non-empty string value found for the provided keys.
func StringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := args[key].(string)
		if ok && value != "" {
			return value
		}
	}
	return ""
}

// ResolvePath resolves path relative to workDir when it is not already absolute.
func ResolvePath(workDir, path string) (string, error) {
	if path == "" {
		path = "."
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if workDir == "" {
		return filepath.Abs(path)
	}
	return filepath.Abs(filepath.Join(workDir, path))
}
