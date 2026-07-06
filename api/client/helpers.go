package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/config"
)

// NewAPIClient creates a client from the STELLA_TOKEN env var and the configured server URL.
func NewAPIClient(extra ...ClientOption) (*Client, error) {
	token := os.Getenv("STELLA_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("STELLA_TOKEN env var is required (set it to a personal access token created in the Web UI)")
	}
	opts := append([]ClientOption{WithRequestEditorFn(BearerAuth(token))}, extra...)
	return NewClient(config.ServerURL(), opts...)
}

// BearerAuth returns a RequestEditorFn that sets the Authorization header.
func BearerAuth(token string) RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// ReadBody reads the response body and returns an error on 4xx/5xx.
func ReadBody(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var apiErr apitypes.Error
		if jerr := json.Unmarshal(body, &apiErr); jerr == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("stella server %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		if snippet == "" {
			return nil, fmt.Errorf("stella server returned %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("stella server %d: %s", resp.StatusCode, snippet)
	}
	return body, nil
}

// DecodeJSON reads the response body and unmarshals it directly into out.
func DecodeJSON(resp *http.Response, out any) error {
	body, err := ReadBody(resp)
	if err != nil {
		return err
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.Unmarshal(body, out)
}

// Call creates an API client, executes call, and decodes the response JSON into T.
func Call[T any](call func(*Client) (*http.Response, error)) (T, error) {
	var zero T
	api, err := NewAPIClient()
	if err != nil {
		return zero, err
	}
	resp, err := call(api)
	if err != nil {
		return zero, WrapServerErr(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var out T
	if err := DecodeJSON(resp, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// Do is like Call but for endpoints where no response body is needed.
func Do(call func(*Client) (*http.Response, error)) error {
	api, err := NewAPIClient()
	if err != nil {
		return err
	}
	resp, err := call(api)
	if err != nil {
		return WrapServerErr(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return DecodeJSON(resp, nil)
}

// WrapServerErr decorates connection errors with a hint about STELLA_SERVER_URL.
func WrapServerErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("call stella server: %w (run 'stellad server' or set STELLA_SERVER_URL)", err)
}

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T { return &v }
