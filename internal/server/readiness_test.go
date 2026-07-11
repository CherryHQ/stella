package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestReadyz(t *testing.T) {
	cases := []struct {
		name    string
		started bool
		drain   bool
		pingErr error
		want    int
	}{
		{name: "not started yet", started: false, want: http.StatusServiceUnavailable},
		{name: "ready", started: true, want: http.StatusOK},
		{name: "draining", started: true, drain: true, want: http.StatusServiceUnavailable},
		{name: "database down", started: true, pingErr: errors.New("connection refused"), want: http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newReadiness(context.Background(), fakePinger{err: tc.pingErr})
			if tc.started {
				r.markStartupComplete()
			}
			if tc.drain {
				r.beginDrain()
			}
			rec := httptest.NewRecorder()
			r.readyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != tc.want {
				t.Errorf("readyz code = %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestHealthzAlwaysOK(t *testing.T) {
	// Healthz must not depend on startup, drain, or the database.
	r := newReadiness(context.Background(), fakePinger{err: errors.New("db down")})
	r.beginDrain()
	rec := httptest.NewRecorder()
	r.healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz code = %d, want 200", rec.Code)
	}
}

func TestBeginDrainReleasesStreamContext(t *testing.T) {
	r := newReadiness(context.Background(), fakePinger{})
	sctx, cancel := r.streamContext(context.Background())
	defer cancel()

	select {
	case <-sctx.Done():
		t.Fatal("stream context cancelled before drain began")
	default:
	}

	r.beginDrain()

	// AfterFunc fires asynchronously; a broken wiring hangs until the test times out.
	<-sctx.Done()
	if !r.isDraining() {
		t.Fatal("isDraining() = false after beginDrain()")
	}
}

func TestBeginDrainIdempotent(t *testing.T) {
	r := newReadiness(context.Background(), fakePinger{})
	r.beginDrain()
	r.beginDrain() // second call must not panic on a re-cancel/re-close
	if !r.isDraining() {
		t.Fatal("isDraining() = false")
	}
}
