package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// Wire protocol: one JSON request per connection, one JSON response, close.
// A fresh connection per call keeps the Python side trivial (no multiplexing)
// and makes concurrent tool calls independent. Payloads are base64 inside JSON;
// per-call size cap below. Ceiling: switch to a streaming transport if eval
// tasks move files large enough for this to matter.
const maxPayloadBytes = 32 << 20

type request struct {
	Nonce      string            `json:"nonce"`
	Op         string            `json:"op"`
	Command    string            `json:"command,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
	Path       string            `json:"path,omitempty"`
	Data       []byte            `json:"data,omitempty"`
	Mode       uint32            `json:"mode,omitempty"`
	Files      []projFile        `json:"files,omitempty"`
}

type projFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
	Mode uint32 `json:"mode"`
}

type dirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type response struct {
	OK         bool       `json:"ok"`
	Error      string     `json:"error,omitempty"`
	Code       string     `json:"code,omitempty"`
	Stdout     string     `json:"stdout,omitempty"`
	Stderr     string     `json:"stderr,omitempty"`
	ReturnCode int        `json:"return_code,omitempty"`
	Data       []byte     `json:"data,omitempty"`
	Entries    []dirEntry `json:"entries,omitempty"`
	IsDir      bool       `json:"is_dir,omitempty"`
	Size       int64      `json:"size,omitempty"`
}

// Error codes the bridge server may return; mapped to fs-style errors so core
// tools report "file not found" the same way they do on other backends.
const (
	codeNotFound = "not_found"
	codeIsDir    = "is_dir"
	codeNonce    = "bad_nonce"
	codeConflict = "conflict"
)

var errBadNonce = errors.New("bridge: nonce rejected by bridge (binding mismatch)")

type client struct {
	socket string
	nonce  string
}

func (c *client) call(ctx context.Context, req request) (response, error) {
	req.Nonce = c.nonce
	if len(req.Data) > maxPayloadBytes {
		return response{}, fmt.Errorf("bridge: payload %d bytes exceeds cap %d", len(req.Data), maxPayloadBytes)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return response{}, fmt.Errorf("bridge: dial %s: %w", c.socket, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else if req.TimeoutSec > 0 {
		// Give the server room to enforce its own timeout first.
		_ = conn.SetDeadline(time.Now().Add(time.Duration(req.TimeoutSec)*time.Second + 30*time.Second))
	}
	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return response{}, fmt.Errorf("bridge: send %s: %w", req.Op, err)
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	var resp response
	dec := json.NewDecoder(r)
	if err := dec.Decode(&resp); err != nil {
		return response{}, fmt.Errorf("bridge: read %s response: %w", req.Op, err)
	}
	if !resp.OK {
		if resp.Code == codeNonce {
			return resp, errBadNonce
		}
		return resp, fmt.Errorf("bridge: %s: %s", req.Op, resp.Error)
	}
	return resp, nil
}
