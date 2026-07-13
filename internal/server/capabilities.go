package server

import "net/http"

// capability names an optional server capability whose backing service may be
// absent (a nil field in server.Deps). Endpoints that need one gate on it and,
// when it is unconfigured, report a uniform 503 through
// writeCapabilityUnavailable — a single place that owns the optional-capability
// degraded-mode contract, so the (capability -> message) mapping is not
// scattered across handlers.
type capability string

const (
	capVault       capability = "vault"
	capMCP         capability = "mcp"
	capScheduler   capability = "scheduler"
	capGoal        capability = "goal"
	capWorkflow    capability = "workflow"
	capPAT         capability = "pat"
	capOAuthServer capability = "oauth_authorization_server"
)

// capabilityUnavailableMessages maps each optional capability to the exact 503
// message its endpoints report when the capability is unconfigured. The strings
// preserve the historical per-endpoint wording so client-visible behavior does
// not change.
var capabilityUnavailableMessages = map[capability]string{
	capVault:       "vault not configured",
	capMCP:         "mcp not configured",
	capScheduler:   "scheduler not available",
	capGoal:        "goals unavailable",
	capWorkflow:    "workflows unavailable",
	capPAT:         "personal access tokens not configured",
	capOAuthServer: "oauth authorization server not configured",
}

// writeCapabilityUnavailable writes the canonical 503 for an optional capability
// that was not configured (its Deps field was nil).
func writeCapabilityUnavailable(w http.ResponseWriter, c capability) {
	msg, ok := capabilityUnavailableMessages[c]
	if !ok {
		msg = string(c) + " not configured"
	}
	writeError(w, http.StatusServiceUnavailable, msg)
}
