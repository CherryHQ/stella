package ml

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Client calls the sidecar over its unix socket. It is safe for concurrent use.
type Client struct {
	http       *http.Client
	socketPath string
}

// NewClient returns a client that dials the sidecar at socketPath. The "host" in
// request URLs is ignored (unix transport), so we use a fixed placeholder.
func NewClient(socketPath string) *Client {
	dialer := &net.Dialer{}
	return &Client{
		socketPath: socketPath,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socketPath)
				},
				DisableCompression: true,
				MaxIdleConns:       8,
				IdleConnTimeout:    60 * time.Second,
			},
		},
	}
}

func (c *Client) url(path string) string { return "http://stella-ml" + path }

// Health fetches /healthz. A transport error means the sidecar is down.
func (c *Client) Health(ctx context.Context) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(pathHealthz), nil)
	if err != nil {
		return Health{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Health{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Health{}, c.errorFrom(resp)
	}
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return Health{}, fmt.Errorf("decode healthz: %w", err)
	}
	return h, nil
}

// Embed returns one vector per text, in order. tenant scopes per-tenant fairness;
// an empty tenant is allowed (the sidecar maps it to a default bucket).
func (c *Client) Embed(ctx context.Context, tenant string, mode Mode, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(struct {
		Texts []string `json:"texts"`
		Mode  string   `json:"mode"`
	}{Texts: texts, Mode: string(mode)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(pathEmbed), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentJSON)
	c.setContext(req, tenant)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, c.errorFrom(resp)
	}

	if pv := resp.Header.Get(headerRespProtocol); pv != "" && pv != ProtocolVersion {
		return nil, fmt.Errorf("sidecar protocol %s, client supports %s", pv, ProtocolVersion)
	}
	dim, err := strconv.Atoi(resp.Header.Get(headerRespDim))
	if err != nil || dim <= 0 {
		return nil, fmt.Errorf("missing/invalid %s header", headerRespDim)
	}
	if n, err := strconv.Atoi(resp.Header.Get(headerRespCount)); err == nil && n != len(texts) {
		return nil, fmt.Errorf("sidecar returned %d vectors, sent %d texts", n, len(texts))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return decodeVectors(raw, dim, len(texts))
}

// Extract sends raw file bytes for text extraction/OCR. mime hints the source
// type; forceOCR skips the text-layer fast path. Available from Phase 4a.
func (c *Client) Extract(ctx context.Context, tenant, mime string, data []byte, forceOCR bool) (ExtractResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(pathExtract), bytes.NewReader(data))
	if err != nil {
		return ExtractResult{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(headerExtMime, mime)
	if forceOCR {
		req.Header.Set(headerExtForce, "1")
	}
	c.setContext(req, tenant)

	resp, err := c.http.Do(req)
	if err != nil {
		return ExtractResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ExtractResult{}, c.errorFrom(resp)
	}
	var out ExtractResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ExtractResult{}, fmt.Errorf("decode extract: %w", err)
	}
	return out, nil
}

// setContext propagates the request-identity headers, including an absolute
// deadline derived from ctx so the sidecar can shed work it can no longer deliver.
func (c *Client) setContext(req *http.Request, tenant string) {
	if tenant != "" {
		req.Header.Set(headerTenant, tenant)
	}
	req.Header.Set(headerRequestID, newRequestID())
	if dl, ok := req.Context().Deadline(); ok {
		req.Header.Set(headerDeadline, strconv.FormatInt(dl.UnixMilli(), 10))
	}
}

// newRequestID returns a short random correlation id echoed in sidecar logs and
// error envelopes. Best-effort: a clock-derived fallback keeps it non-empty.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func (c *Client) errorFrom(resp *http.Response) error {
	var e errorBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e); err == nil && e.Error != "" {
		return fmt.Errorf("stella-ml %d: %s", resp.StatusCode, e.Error)
	}
	return fmt.Errorf("stella-ml %d", resp.StatusCode)
}

// decodeVectors splits a little-endian float32 blob into n vectors of dim each.
func decodeVectors(raw []byte, dim, n int) ([][]float32, error) {
	want := n * dim * 4
	if len(raw) != want {
		return nil, fmt.Errorf("embed response is %d bytes, want %d (n=%d dim=%d)", len(raw), want, n, dim)
	}
	out := make([][]float32, n)
	for i := range n {
		v := make([]float32, dim)
		for d := range dim {
			v[d] = math.Float32frombits(binary.LittleEndian.Uint32(raw[(i*dim+d)*4:]))
		}
		out[i] = v
	}
	return out, nil
}
