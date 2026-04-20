package admin_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestListBuiltinTemplates(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/builtin/template", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var list []map[string]any
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("expected builtin templates, got none")
	}
	for _, item := range list {
		if _, ok := item["content"]; ok {
			t.Errorf("list response should not include content: %v", item)
		}
		if item["id"] == "" || item["name"] == "" {
			t.Errorf("list item missing id/name: %v", item)
		}
	}
}

func TestGetBuiltinTemplate(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/builtin/template/coder", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d (%s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var full map[string]any
	if err := json.Unmarshal(resp.Data, &full); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if full["id"] != "coder" {
		t.Errorf("id = %v, want coder", full["id"])
	}
	if full["content"] == "" {
		t.Errorf("content should be populated")
	}
}

func TestBuiltinUnknownKind(t *testing.T) {
	env := setupAdmin(t)
	rr := doRequest(t, env, "GET", "/api/builtin/bogus", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown kind: %d, want 404", rr.Code)
	}
}

func TestBuiltinUnknownID(t *testing.T) {
	env := setupAdmin(t)
	rr := doRequest(t, env, "GET", "/api/builtin/template/does-not-exist", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d, want 404", rr.Code)
	}
}
