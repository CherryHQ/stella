package mcpfixture

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	fixtureRoutePrefix = "/mcp/"
	maxTrialLength     = 64
	maxRequestBytes    = 1 << 20
)

type routeContextKey struct{}

// RouteFromContext returns the opaque HMAC route accepted for an MCP request.
// Tool handlers use it only to attribute fixture evidence to that route.
func RouteFromContext(ctx context.Context) string {
	route, _ := ctx.Value(routeContextKey{}).(string)
	return route
}

// RouteForTrial derives the opaque fixture route for one evaluator trial.
func RouteForTrial(routeKey []byte, trial string) (string, error) {
	if len(routeKey) != 32 || len(trial) == 0 || len(trial) > maxTrialLength {
		return "", errInvalidRoute
	}
	payload := append([]byte{byte(len(trial))}, []byte(trial)...)
	mac := hmac.New(sha256.New, routeKey)
	_, _ = mac.Write(payload)
	payload = append(payload, mac.Sum(nil)[:16]...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// RouteObserver receives only allowlisted JSON-RPC requests after their HMAC
// route and session binding have been checked. It must not retain tool input.
type RouteObserver func(route, method string)

// NewStreamableHTTPHandler serves the fixture's intentionally small MCP
// surface. It accepts exactly the initialization, cancellation, and tool
// methods the official Go SDK needs here; every other JSON-RPC method is
// rejected before it reaches the SDK handler.
func NewStreamableHTTPHandler(routeKey []byte, server *mcpsdk.Server, observe RouteObserver) http.Handler {
	return &streamableHandler{
		routeKey: routeKey,
		mcp: mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
			return server
		}, nil),
		observe:       observe,
		sessionRoutes: make(map[string]string),
	}
}

type streamableHandler struct {
	routeKey      []byte
	mcp           http.Handler
	observe       RouteObserver
	mu            sync.Mutex
	sessionRoutes map[string]string
}

func (h *streamableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := h.routeSegment(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	sessionID := r.Header.Get("Mcp-Session-Id")
	switch r.Method {
	case http.MethodGet:
		if !h.routeOwnsSession(route, sessionID) {
			http.NotFound(w, r)
			return
		}
		h.mcp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routeContextKey{}, route)))
	case http.MethodDelete:
		if !h.routeOwnsSession(route, sessionID) {
			http.NotFound(w, r)
			return
		}
		h.mcp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routeContextKey{}, route)))
		h.removeSession(sessionID)
	case http.MethodPost:
		body, err := readRequestBody(r)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		method, isNotification, ok := allowedMethod(body, sessionID != "")
		if !ok || (isNotification && sessionID == "") {
			http.NotFound(w, r)
			return
		}
		if method == "initialize" {
			if sessionID != "" {
				http.NotFound(w, r)
				return
			}
		} else if !h.routeOwnsSession(route, sessionID) {
			http.NotFound(w, r)
			return
		}
		if h.observe != nil {
			h.observe(route, method)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.mcp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routeContextKey{}, route)))
		if method == "initialize" {
			h.rememberSession(route, w.Header().Get("Mcp-Session-Id"))
		}
	default:
		http.NotFound(w, r)
	}
}

func (h *streamableHandler) routeSegment(r *http.Request) (string, bool) {
	if r.URL.RawQuery != "" || r.URL.Path == "" || strings.Contains(strings.ToLower(r.URL.EscapedPath()), "%2f") || strings.Contains(strings.ToLower(r.URL.EscapedPath()), "%5c") || !strings.HasPrefix(r.URL.Path, fixtureRoutePrefix) {
		return "", false
	}
	route := strings.TrimPrefix(r.URL.Path, fixtureRoutePrefix)
	if route == "" || strings.Contains(route, "/") || route == "." || route == ".." || strings.Contains(route, "\\") {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(route)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != route {
		return "", false
	}
	trialLen := 0
	if len(decoded) > 0 {
		trialLen = int(decoded[0])
	}
	if trialLen == 0 || trialLen > maxTrialLength || len(decoded) != 1+trialLen+16 {
		return "", false
	}
	mac := hmac.New(sha256.New, h.routeKey)
	_, _ = mac.Write(decoded[:1+trialLen])
	if !hmac.Equal(decoded[1+trialLen:], mac.Sum(nil)[:16]) {
		return "", false
	}
	return route, true
}

func (h *streamableHandler) routeOwnsSession(route, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessionRoutes[sessionID] == route
}

func (h *streamableHandler) rememberSession(route, sessionID string) {
	if sessionID == "" {
		return
	}
	h.mu.Lock()
	h.sessionRoutes[sessionID] = route
	h.mu.Unlock()
}

func (h *streamableHandler) removeSession(sessionID string) {
	h.mu.Lock()
	delete(h.sessionRoutes, sessionID)
	h.mu.Unlock()
}

func readRequestBody(r *http.Request) ([]byte, error) {
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		return nil, errInvalidRequest
	}
	return body, nil
}

func allowedMethod(body []byte, hasSession bool) (method string, notification, ok bool) {
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.JSONRPC != "2.0" {
		return "", false, false
	}
	notification = len(envelope.ID) == 0
	switch envelope.Method {
	case "initialize":
		return envelope.Method, false, !notification && !hasSession
	case "notifications/initialized", "notifications/cancelled":
		return envelope.Method, true, notification && hasSession
	case "tools/list", "tools/call":
		return envelope.Method, false, !notification && hasSession
	default:
		return "", false, false
	}
}

type routeError string

func (e routeError) Error() string { return string(e) }

const (
	errInvalidRoute   = routeError("invalid fixture route")
	errInvalidRequest = routeError("invalid fixture request")
)
