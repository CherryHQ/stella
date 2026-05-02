// Package recallyclient is the generated + hand-rolled HTTP client for the
// recally REST API. The generated client lives in client_gen.go; this file
// adds small ergonomics on top: bearer auth, JSON decoding, and a constructor
// that reads ANNA_TOKEN/ANNA_SERVER_URL from the environment.
package recallyclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/vaayne/anna/internal/config"
)

// NewFromEnv builds a Client pointed at config.ServerURL() and authenticated
// with ANNA_TOKEN. Returns an error if ANNA_TOKEN is unset.
func NewFromEnv(extra ...ClientOption) (*Client, error) {
	token := os.Getenv("ANNA_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("ANNA_TOKEN env var is required (run 'anna serve' and set ANNA_TOKEN to a generated token)")
	}
	opts := append([]ClientOption{WithRequestEditorFn(BearerAuth(token))}, extra...)
	return NewClient(config.ServerURL(), opts...)
}

// BearerAuth returns a RequestEditorFn that adds an Authorization: Bearer
// header to every request.
func BearerAuth(token string) RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// DecodeJSON reads the response body, decodes JSON into out, and converts
// non-2xx responses into an error carrying the API error message.
func DecodeJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var apiErr Error
		if jerr := json.Unmarshal(body, &apiErr); jerr == nil && apiErr.Error != "" {
			return fmt.Errorf("anna server %d: %s", resp.StatusCode, apiErr.Error)
		}
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		if snippet == "" {
			return fmt.Errorf("anna server returned %d", resp.StatusCode)
		}
		return fmt.Errorf("anna server %d: %s", resp.StatusCode, snippet)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Ptr returns a pointer to v. Convenience for filling in *string / *int /
// *bool params from CLI flags.
func Ptr[T any](v T) *T { return &v }
