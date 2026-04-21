//go:build darwin

package local

import (
	"fmt"
	"os/exec"
	"strings"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

// seatbeltProfile is the macOS Seatbelt (sandbox-exec) profile template.
// WORKSPACE_ROOT is replaced with the actual workspace path.
// NETWORK_RULE is replaced with the appropriate network allow/deny rule.
//
// Seatbelt evaluates rules in order; the last matching rule wins.
// Strategy: allow file-read* for all paths, then deny specific credential paths
// after the broad allow so the deny takes precedence. The workspace root read
// allow comes last to re-permit workspace reads even if the workspace is under
// a denied prefix (e.g. /Users). Write access is limited to workspace and /tmp.
const seatbeltProfile = `(version 1)
(deny default)
(allow process-exec)
(allow process-fork)
(allow file-read* (subpath "/"))
(deny file-read* (subpath "/Users"))
(deny file-read* (subpath "/home"))
(allow file-read* (subpath "WORKSPACE_ROOT"))
(allow file-write* (subpath "WORKSPACE_ROOT"))
(allow file-write* (subpath "/tmp"))
(allow file-write* (subpath "/private/tmp"))
(deny file-write* (subpath "/Users"))
(deny file-write* (subpath "/home"))
(allow sysctl-read)
NETWORK_RULE
`

// wrapCommand wraps name+args with sandbox-exec using a Seatbelt profile on
// macOS. If sandbox-exec is not available, an actionable error is returned.
func wrapCommand(policy sandboxpkg.Policy, name string, args []string) (string, []string, error) {
	sandboxExecPath, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return "", nil, fmt.Errorf(
			"local sandbox: sandbox-exec unavailable on this macOS version; " +
				"use the docker backend for isolation",
		)
	}

	workspaceRoot := policy.WorkspaceRootOrDefault()
	networkMode := policy.NetworkModeOrDefault()

	var networkRule string
	if networkMode == sandboxpkg.NetworkAllowAll {
		networkRule = "(allow network*)"
	} else {
		networkRule = "(deny network*)"
	}

	// Escape special characters in the workspace root path for the profile.
	escapedRoot := strings.ReplaceAll(workspaceRoot, `\`, `\\`)
	escapedRoot = strings.ReplaceAll(escapedRoot, `"`, `\"`)

	profile := strings.ReplaceAll(seatbeltProfile, "WORKSPACE_ROOT", escapedRoot)
	profile = strings.ReplaceAll(profile, "NETWORK_RULE", networkRule)

	sandboxArgs := []string{"-p", profile, name}
	sandboxArgs = append(sandboxArgs, args...)
	return sandboxExecPath, sandboxArgs, nil
}
