package server

import (
	"encoding/json"
	"net/http"
)

// writeData writes a success JSON response with the given data.
func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeListData writes a success JSON response wrapping items in {"items": ...}.
func writeListData(w http.ResponseWriter, status int, items any) {
	writeData(w, status, map[string]any{"items": items})
}

// writeError writes a structured error response per AIP-193:
// {"error": {"code", "message", "status"}}.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": msg,
			"status":  statusName(status),
		},
	})
}

// statusName maps an HTTP status code to its canonical AIP status name.
func statusName(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "ABORTED"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusBadGateway:
		return "UNAVAILABLE"
	case http.StatusInternalServerError:
		return "INTERNAL"
	default:
		return "UNKNOWN"
	}
}

// writeNoContent writes a 204 No Content response with no body.
func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON reads a JSON body into dst.
func decodeJSON(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}
