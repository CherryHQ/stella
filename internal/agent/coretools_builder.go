package agent

import (
	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/vision"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// buildSandboxCoreTools creates core tools using the active sandbox session.
func buildSandboxCoreTools(session pkgsandbox.Session, sessionSecretValues *agentsandbox.SessionSecretValues, visionSvc *vision.Service) []pkgtools.Tool {
	if session == nil {
		return nil
	}
	return agentsandbox.NewTools(session, sessionSecretValues, visionSvc)
}
