//go:build system

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// testProviderCatalogCAS proves the two provider-management safety promises at
// the real process boundary: probing an invalid adapter never inserts a row,
// and a stale settings write cannot overwrite the first writer.
func (h *harness) testProviderCatalogCAS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var before int
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM provider").Scan(&before); err != nil {
		t.Fatalf("count providers before probe: %v", err)
	}
	probe := h.jsonRequest(t, ctx, http.MethodPost, "/api/providers/probe", map[string]any{
		"api_type": "not-a-provider",
		"api_key":  "invalid",
		"base_url": "https://example.invalid",
	})
	if probe.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid provider probe = %d, want 400\n%s", probe.StatusCode, h.proc.logTail(30))
	}
	_ = probe.Body.Close()
	var after int
	if err := h.db.QueryRow(ctx, "SELECT count(*) FROM provider").Scan(&after); err != nil {
		t.Fatalf("count providers after probe: %v", err)
	}
	if after != before {
		t.Fatalf("invalid probe inserted provider rows: before=%d after=%d", before, after)
	}

	providerID := "system-cas-" + h.runID
	created := h.jsonRequest(t, ctx, http.MethodPost, "/api/providers", map[string]any{
		"id": providerID, "type": "openai", "name": "system CAS", "enabled": true, "api_key": "invalid",
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create provider = %d\n%s", created.StatusCode, h.proc.logTail(30))
	}
	_ = created.Body.Close()

	original := h.providerVersion(t, ctx, providerID)
	first := h.jsonRequest(t, ctx, http.MethodPatch, "/api/providers/"+providerID, map[string]any{
		"name": "first writer", "expected_version": original,
	})
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first provider write = %d\n%s", first.StatusCode, h.proc.logTail(30))
	}
	_ = first.Body.Close()

	stale := h.jsonRequest(t, ctx, http.MethodPatch, "/api/providers/"+providerID, map[string]any{
		"name": "stale writer", "expected_version": original,
	})
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale provider write = %d, want 409\n%s", stale.StatusCode, h.proc.logTail(30))
	}
	_ = stale.Body.Close()

	verify := h.jsonRequest(t, ctx, http.MethodGet, "/api/providers/"+providerID, nil)
	defer func() { _ = verify.Body.Close() }()
	if verify.StatusCode != http.StatusOK {
		t.Fatalf("verify provider = %d", verify.StatusCode)
	}
	var envelope struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(verify.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	if envelope.Data.Name != "first writer" {
		t.Fatalf("provider name = %q, want first writer", envelope.Data.Name)
	}
}

func (h *harness) providerVersion(t *testing.T, ctx context.Context, id string) string {
	t.Helper()
	resp := h.jsonRequest(t, ctx, http.MethodGet, "/api/providers/"+id, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET provider = %d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode provider version: %v", err)
	}
	if envelope.Data.Version == "" {
		t.Fatal("provider response has empty version")
	}
	return envelope.Data.Version
}

func (h *harness) jsonRequest(t *testing.T, ctx context.Context, method, route string, body any) *http.Response {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, route, err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+route, payload)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, route, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", method, route, err, h.proc.logTail(30))
	}
	return resp
}
