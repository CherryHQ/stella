package server

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeNativeJSONRejectsLooseBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"agent_id":"agent-1","extra":true}`},
		{name: "trailing document", body: `{"agent_id":"agent-1"}{}`},
		{name: "oversized", body: `{"agent_id":"` + strings.Repeat("x", maxNativePolicyBodyBytes) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			var dst struct {
				AgentID *string `json:"agent_id"`
			}
			if err := decodeNativeJSON(rec, req, &dst); err == nil {
				t.Fatal("decode succeeded for invalid body")
			}
		})
	}
}

func TestDecodeNativeJSONPreservesFalseAndRejectsNullField(t *testing.T) {
	validReq := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"is_enabled":false}`))
	validRec := httptest.NewRecorder()
	var valid struct {
		IsEnabled *bool `json:"is_enabled"`
	}
	if err := decodeNativeJSON(validRec, validReq, &valid); err != nil {
		t.Fatalf("decode false: %v", err)
	}
	if valid.IsEnabled == nil || *valid.IsEnabled {
		t.Fatalf("decoded false = %#v, want non-nil false", valid.IsEnabled)
	}

	nullReq := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"is_enabled":null}`))
	nullRec := httptest.NewRecorder()
	var nulled struct {
		IsEnabled *bool `json:"is_enabled"`
	}
	if err := decodeNativeJSON(nullRec, nullReq, &nulled); err != nil {
		t.Fatalf("decode null syntax: %v", err)
	}
	if nulled.IsEnabled != nil {
		t.Fatalf("decoded null = %#v, want nil", nulled.IsEnabled)
	}
}

func TestNativePageWindowSlicesOffsetAndProbe(t *testing.T) {
	rows := []string{"one", "two", "three", "four", "five"}

	if got := nativePageWindow(rows, 2, 0); strings.Join(got, ",") != "one,two,three" {
		t.Fatalf("first probe window = %v", got)
	}
	if got := nativePageWindow(rows, 2, 2); strings.Join(got, ",") != "three,four,five" {
		t.Fatalf("second probe window = %v", got)
	}
	if got := nativePageWindow(rows, 2, 5); got != nil {
		t.Fatalf("terminal probe window = %v, want nil", got)
	}
}
