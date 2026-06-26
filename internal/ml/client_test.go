package ml

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeSidecar serves a canned protocol on a unix socket for client tests.
func fakeSidecar(t *testing.T, h http.Handler) (*Client, func()) {
	t.Helper()
	// Keep the socket path short; macOS caps unix paths near 104 bytes and t.TempDir
	// can already be long.
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: h}}
	srv.Start()
	return NewClient(sock), srv.Close
}

func writeFloatBlob(w http.ResponseWriter, vecs [][]float32) {
	w.Header().Set(headerRespDim, strconv.Itoa(len(vecs[0])))
	w.Header().Set(headerRespCount, strconv.Itoa(len(vecs)))
	w.Header().Set(headerRespProtocol, ProtocolVersion)
	w.WriteHeader(http.StatusOK)
	var buf []byte
	for _, v := range vecs {
		for _, f := range v {
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(f))
		}
	}
	_, _ = w.Write(buf)
}

func TestClientHealth(t *testing.T) {
	c, stop := fakeSidecar(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Health{Status: "ok", ProtocolVersion: ProtocolVersion, RuntimeVersion: "v1"})
	}))
	defer stop()

	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !h.Ready() {
		t.Fatalf("expected ready, got %+v", h)
	}
}

func TestClientEmbedDecodes(t *testing.T) {
	want := [][]float32{{1, 2, 3}, {-1, 0.5, 4}}
	var gotTenant string
	c, stop := fakeSidecar(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get(headerTenant)
		if r.Header.Get(headerRequestID) == "" {
			t.Errorf("missing request id header")
		}
		writeFloatBlob(w, want)
	}))
	defer stop()

	got, err := c.Embed(context.Background(), "acme", ModeQuery, []string{"a", "b"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if gotTenant != "acme" {
		t.Errorf("tenant header = %q, want acme", gotTenant)
	}
	if len(got) != 2 || got[0][2] != 3 || got[1][0] != -1 {
		t.Fatalf("decoded vectors wrong: %v", got)
	}
}

func TestClientEmbedCountMismatch(t *testing.T) {
	c, stop := fakeSidecar(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeFloatBlob(w, [][]float32{{1, 2, 3}}) // 1 vector for 2 texts
	}))
	defer stop()

	if _, err := c.Embed(context.Background(), "", ModeQuery, []string{"a", "b"}); err == nil {
		t.Fatal("expected count-mismatch error")
	}
}

func TestClientEmbedProtocolMismatch(t *testing.T) {
	c, stop := fakeSidecar(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerRespProtocol, "999")
		w.Header().Set(headerRespDim, "3")
		w.Header().Set(headerRespCount, "1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 12))
	}))
	defer stop()

	if _, err := c.Embed(context.Background(), "", ModeQuery, []string{"a"}); err == nil {
		t.Fatal("expected protocol-mismatch error")
	}
}
