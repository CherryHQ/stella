// Package ml is the stellad-side client and supervisor for the native ML sidecar
// (cmd/stella-ml). stellad stays CGO_ENABLED=0 and talks to the sidecar over an
// HTTP-on-unix-socket contract; this package owns spawning it, health-gating it,
// restarting it on crash, and calling its endpoints.
package ml

// Protocol contract — the stellad-side copy. KEEP IN SYNC with the sidecar's
// cmd/stella-ml/protocol.go. The /healthz version handshake catches drift: the
// client refuses a sidecar whose protocol version it does not support.
const (
	ProtocolVersion = "1"

	pathHealthz = "/healthz"
	pathEmbed   = "/v1/embed"
	pathExtract = "/v1/extract"

	headerTenant    = "X-Stella-Tenant"
	headerRequestID = "X-Stella-Request-Id"
	headerDeadline  = "X-Stella-Deadline-Unix-Ms"

	// Extract sends mode-equivalent hints as headers; embed sends mode in the JSON
	// body. The sidecar also accepts an X-Stella-Mode header as a fallback, which
	// this client does not use.
	headerExtMime  = "X-Stella-Mime"
	headerExtForce = "X-Stella-Force-Ocr"

	// Response headers the client validates (the sidecar also sets X-Stella-ML-Model).
	headerRespDim      = "X-Stella-ML-Dim"
	headerRespCount    = "X-Stella-ML-Count"
	headerRespProtocol = "X-Stella-ML-Protocol"

	contentJSON = "application/json"
)

// Mode is the e5 instruction prefix selector. Index text is a passage, search
// text is a query.
type Mode string

const (
	ModeQuery   Mode = "query"
	ModePassage Mode = "passage"
)

// Health is the parsed /healthz response.
type Health struct {
	Status              string            `json:"status"`
	RuntimeVersion      string            `json:"runtime_version"`
	ProtocolVersion     string            `json:"protocol_version"`
	ModelManifestDigest string            `json:"model_manifest_digest"`
	Models              map[string]string `json:"models"`
}

// Ready reports whether the sidecar has loaded its engines and speaks a protocol
// version this client supports.
func (h Health) Ready() bool {
	return h.Status == "ok" && h.ProtocolVersion == ProtocolVersion
}

type errorBody struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

// ExtractResult mirrors the sidecar's /v1/extract JSON response.
type ExtractResult struct {
	Content  string `json:"content"`
	MimeType string `json:"mime_type"`
}
