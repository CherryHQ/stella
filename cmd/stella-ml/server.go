package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"
)

// server hosts the ML engines behind the UDS HTTP contract. extract is nil until
// the OCR engine lands (Phase 4a); its endpoint returns 501 until then.
type server struct {
	embed   *e5Engine
	lim     limits
	embedLn *lane
	extLn   *lane

	runtimeVersion string
	modelDigest    string
	log            *slog.Logger
}

func newServer(embed *e5Engine, runtimeVersion, modelDigest string, lim limits, embedSlots, extractSlots int, log *slog.Logger) *server {
	return &server{
		embed:          embed,
		lim:            lim,
		embedLn:        newLane(embedSlots, lim.perTenantInflight),
		extLn:          newLane(extractSlots, lim.perTenantInflight),
		runtimeVersion: runtimeVersion,
		modelDigest:    modelDigest,
		log:            log,
	}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+pathHealthz, s.handleHealthz)
	mux.HandleFunc("POST "+pathEmbed, s.handleEmbed)
	mux.HandleFunc("POST "+pathExtract, s.handleExtract)
	return mux
}

// reqContext carries the per-request identity/deadline parsed from headers.
type reqContext struct {
	tenant    string
	requestID string
}

// withContext parses the request-context headers, applies the deadline to ctx,
// and returns both. A missing tenant maps to "anonymous" so fairness still has a
// key; a missing/invalid deadline leaves ctx unbounded (the server's own timeouts
// still apply).
func (s *server) withContext(r *http.Request) (*http.Request, reqContext, context.CancelFunc) {
	rc := reqContext{
		tenant:    valueOr(r.Header.Get(headerTenant), "anonymous"),
		requestID: r.Header.Get(headerRequestID),
	}
	ctx := r.Context()
	cancel := context.CancelFunc(func() {})
	if ms := r.Header.Get(headerDeadline); ms != "" {
		if unixMs, err := strconv.ParseInt(ms, 10, 64); err == nil && unixMs > 0 {
			ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(unixMs))
		}
	}
	return r.WithContext(ctx), rc, cancel
}

func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	status := "warming"
	models := map[string]string{}
	if s.embed != nil {
		status = "ok"
		models["embed"] = embedModelID
	}
	writeJSON(w, http.StatusOK, healthResponse{
		Status:              status,
		RuntimeVersion:      s.runtimeVersion,
		ProtocolVersion:     ProtocolVersion,
		ModelManifestDigest: s.modelDigest,
		Models:              models,
	})
}

func (s *server) handleEmbed(w http.ResponseWriter, r *http.Request) {
	r, rc, cancel := s.withContext(r)
	defer cancel()

	if s.embed == nil {
		s.fail(w, rc, http.StatusServiceUnavailable, errors.New("embed engine not loaded"))
		return
	}

	var req embedRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, s.lim.maxExtractBody)).Decode(&req); err != nil {
		s.fail(w, rc, http.StatusBadRequest, errwrap("decode embed request", err))
		return
	}
	if len(req.Texts) == 0 {
		s.fail(w, rc, http.StatusBadRequest, errors.New("no texts"))
		return
	}
	if len(req.Texts) > s.lim.maxEmbedBatch {
		s.fail(w, rc, http.StatusRequestEntityTooLarge, errors.New("batch exceeds max "+strconv.Itoa(s.lim.maxEmbedBatch)))
		return
	}
	for _, t := range req.Texts {
		if len(t) > s.lim.maxTextBytes {
			s.fail(w, rc, http.StatusRequestEntityTooLarge, errors.New("text exceeds max bytes"))
			return
		}
	}

	mode := req.Mode
	if mode == "" {
		mode = r.Header.Get(headerMode)
	}

	release, err := s.embedLn.acquire(r.Context(), rc.tenant)
	if err != nil {
		s.failAdmission(w, rc, err)
		return
	}
	defer release()

	vecs, err := s.embed.EmbedBatch(modePrefix(mode), req.Texts)
	if err != nil {
		s.fail(w, rc, http.StatusInternalServerError, errwrap("embed", err))
		return
	}

	w.Header().Set("Content-Type", contentOctetStream)
	w.Header().Set(headerRespModel, embedModelID)
	w.Header().Set(headerRespDim, strconv.Itoa(embedDim))
	w.Header().Set(headerRespCount, strconv.Itoa(len(vecs)))
	w.Header().Set(headerRespProtocol, ProtocolVersion)
	w.WriteHeader(http.StatusOK)
	if err := writeVectors(w, vecs); err != nil {
		s.log.Warn("write embed response", "request_id", rc.requestID, "err", err)
	}
}

func (s *server) handleExtract(w http.ResponseWriter, r *http.Request) {
	_, rc, cancel := s.withContext(r)
	defer cancel()
	// OCR engine lands in Phase 4a; the endpoint exists now so the protocol and
	// supervisor contract can be exercised end-to-end.
	s.fail(w, rc, http.StatusNotImplemented, errors.New("extract not implemented yet"))
}

// writeVectors streams vectors as little-endian float32, count*dim contiguous.
func writeVectors(w io.Writer, vecs [][]float32) error {
	buf := make([]byte, 0, len(vecs)*embedDim*4)
	for _, v := range vecs {
		for _, f := range v {
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(f))
		}
	}
	_, err := w.Write(buf)
	return err
}

func (s *server) fail(w http.ResponseWriter, rc reqContext, code int, err error) {
	if code >= 500 {
		s.log.Error("request failed", "request_id", rc.requestID, "tenant", rc.tenant, "code", code, "err", err)
	}
	writeJSON(w, code, errorBody{Error: err.Error(), RequestID: rc.requestID})
}

func (s *server) failAdmission(w http.ResponseWriter, rc reqContext, err error) {
	if errors.Is(err, errBusy) {
		s.fail(w, rc, http.StatusTooManyRequests, err)
		return
	}
	// context deadline/cancel exceeded while queued
	s.fail(w, rc, http.StatusServiceUnavailable, errwrap("admission", err))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", contentJSON)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func errwrap(msg string, err error) error {
	return errors.New(msg + ": " + err.Error())
}
