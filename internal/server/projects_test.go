package server

import (
	"bytes"
	"net/http/httptest"
	"testing"
)

func TestDecodeCreateProjectRequestDistinguishesOmittedAndRootPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantPath *string
	}{
		{name: "omitted", body: `{"name":"root"}`},
		{name: "explicit root", body: `{"name":"root","path":""}`, wantPath: projectPathPtr("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(tc.body))
			got, err := decodeCreateProjectRequest(req)
			if err != nil || got.name != "root" {
				t.Fatalf("decode = %#v, %v", got, err)
			}
			if (got.path == nil) != (tc.wantPath == nil) || got.path != nil && *got.path != *tc.wantPath {
				t.Fatalf("path = %#v, want %#v", got.path, tc.wantPath)
			}
		})
	}
}

func projectPathPtr(value string) *string { return &value }
