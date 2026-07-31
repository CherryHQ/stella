package sandbox

import (
	"path"
	"strings"
)

// POSIXPathRelative returns target relative to root when target is contained in
// root after POSIX normalization. It accepts Windows-style separators at this
// Linux-container boundary and returns "." for root itself.
func POSIXPathRelative(root, target string) (string, bool) {
	root = cleanPOSIXPath(root)
	target = cleanPOSIXPath(target)
	if root == "." || target == "." {
		return "", false
	}
	if target == root {
		return ".", true
	}
	if root == "/" {
		after, ok := strings.CutPrefix(target, "/")
		if ok && after != "" {
			return after, true
		}
		return "", false
	}
	after, ok := strings.CutPrefix(target, root+"/")
	if ok && after != "" {
		return after, true
	}
	return "", false
}

func cleanPOSIXPath(value string) string {
	return path.Clean(strings.ReplaceAll(value, "\\", "/"))
}
