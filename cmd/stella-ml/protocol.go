package main

// Protocol contract between `stellad` (pure-Go client) and this sidecar.
//
// This is a deliberate, tiny duplicate of the client-side copy in
// `internal/ml/protocol.go`. The two modules do not share a package (that would
// force the sidecar to require the entire main module); instead the runtime
// version handshake on /healthz catches any drift. KEEP THE TWO COPIES IN SYNC.
const (
	// ProtocolVersion is bumped on any breaking wire change. The client refuses a
	// sidecar whose protocol version is outside its supported range.
	ProtocolVersion = "1"

	pathHealthz = "/healthz"
	pathEmbed   = "/v1/embed"
	pathExtract = "/v1/extract"

	// Request-context headers. Every request carries them; the sidecar keys its
	// per-tenant fairness and deadlines off these instead of inferring identity.
	headerTenant    = "X-Stella-Tenant"           // account key for per-tenant fairness
	headerRequestID = "X-Stella-Request-Id"       // correlation id, echoed in logs/errors
	headerDeadline  = "X-Stella-Deadline-Unix-Ms" // absolute deadline, same-host clock

	// Embed-specific request/response headers.
	headerMode     = "X-Stella-Mode"      // "query" | "passage" (e5 instruction prefix)
	headerExtMime  = "X-Stella-Mime"      // extract: source mime of the octet-stream body
	headerExtForce = "X-Stella-Force-Ocr" // extract: "1" forces OCR past the text layer

	// Response headers.
	headerRespModel    = "X-Stella-ML-Model"
	headerRespDim      = "X-Stella-ML-Dim"
	headerRespCount    = "X-Stella-ML-Count"
	headerRespProtocol = "X-Stella-ML-Protocol"

	contentOctetStream = "application/octet-stream"
	contentJSON        = "application/json"
)

// errorBody is the JSON error envelope returned for any non-2xx response.
type errorBody struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

// embedRequest is the JSON body of POST /v1/embed.
type embedRequest struct {
	Texts []string `json:"texts"`
	Mode  string   `json:"mode"` // "query" | "passage"; falls back to header, then "query"
}

// healthResponse is the JSON body of GET /healthz.
type healthResponse struct {
	Status              string            `json:"status"` // "ok" | "warming"
	RuntimeVersion      string            `json:"runtime_version"`
	ProtocolVersion     string            `json:"protocol_version"`
	ModelManifestDigest string            `json:"model_manifest_digest"`
	Models              map[string]string `json:"models"` // logical name -> model id
}

// extractResponse is the JSON body of POST /v1/extract.
type extractResponse struct {
	Content  string `json:"content"`
	MimeType string `json:"mime_type"`
}
