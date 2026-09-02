// Package server is the HTTP surface: the REST API generated from
// api/spec, SSE streaming, and the embedded React SPA. It holds no persistence
// store or query handle — it is handed an immutable server.Deps of application
// services and maps their typed errors onto status codes.
package server
