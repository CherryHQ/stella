package fsops

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	ProtocolVersion = 1
	maxFrameBytes   = 1 << 20
)

// Response kinds give each reply a self-describing shape so read/stat/list and
// mutation success schemas are disjoint: an empty list is not confusable with a
// mutation, and a helper that returns the wrong shape fails closed.
const (
	KindRead               = "read"
	KindStat               = "stat"
	KindList               = "list"
	KindMutation           = "mutation"
	KindManagedSkillTarget = "managed_skill_target"
)

// Error codes are a closed, stable set carried on error replies so a remote
// client can reconstruct the same typed error the in-process providers return.
// The helper classifies every failure against io/fs sentinels; unknown codes on
// the wire fail closed. "opaque" is the deliberate catch-all for errors that map
// to no sentinel.
const (
	ErrorCodeNotExist   = "not_exist"
	ErrorCodePermission = "permission"
	ErrorCodeExist      = "exist"
	ErrorCodeReadLimit  = "read_limit"
	ErrorCodeOpaque     = "opaque"
)

// classifyErrorCode maps a helper error to its stable wire code. read_limit is
// checked first because it is the one non-fs sentinel we surface.
func classifyErrorCode(err error) string {
	switch {
	case errors.Is(err, sandbox.ErrReadLimit):
		return ErrorCodeReadLimit
	case errors.Is(err, fs.ErrNotExist):
		return ErrorCodeNotExist
	case errors.Is(err, fs.ErrPermission):
		return ErrorCodePermission
	case errors.Is(err, fs.ErrExist):
		return ErrorCodeExist
	default:
		return ErrorCodeOpaque
	}
}

// sentinelForCode returns the sentinel a client wraps so errors.Is matches the
// in-process providers, or nil for opaque/unmapped codes.
func sentinelForCode(code string) error {
	switch code {
	case ErrorCodeNotExist:
		return fs.ErrNotExist
	case ErrorCodePermission:
		return fs.ErrPermission
	case ErrorCodeExist:
		return fs.ErrExist
	case ErrorCodeReadLimit:
		return sandbox.ErrReadLimit
	default:
		return nil
	}
}

func isKnownErrorCode(code string) bool {
	switch code {
	case "", ErrorCodeNotExist, ErrorCodePermission, ErrorCodeExist, ErrorCodeReadLimit, ErrorCodeOpaque:
		return true
	default:
		return false
	}
}

// KindForOperation maps a request operation to its response kind so a client
// can tell DecodeResponse which reply shape to expect.
func KindForOperation(op string) string {
	switch op {
	case "read":
		return KindRead
	case "stat":
		return KindStat
	case "list":
		return KindList
	case "managed_skill_target":
		return KindManagedSkillTarget
	default: // write, upload, mkdir, remove, rename
		return KindMutation
	}
}

// Request metadata is one bounded frame followed, for write/upload only, by
// exactly BodyLength raw bytes. Bodies never enter JSON or helper memory.
type Request struct {
	Version    int         `json:"version"`
	Operation  string      `json:"operation"`
	Path       string      `json:"path,omitempty"`
	NewPath    string      `json:"new_path,omitempty"`
	Recursive  bool        `json:"recursive,omitempty"`
	Perm       fs.FileMode `json:"perm,omitempty"`
	MaxBytes   int64       `json:"max_bytes,omitempty"`
	BodyLength int64       `json:"body_length,omitempty"`
}

type Response struct {
	Version    int                `json:"version"`
	Kind       string             `json:"kind"`
	Info       sandbox.FileInfo   `json:"info,omitempty"`
	Entries    []sandbox.DirEntry `json:"entries,omitempty"`
	BodyLength int64              `json:"body_length,omitempty"`
	ErrorCode  string             `json:"error_code,omitempty"`
	Error      string             `json:"error,omitempty"`
	Managed    bool               `json:"managed,omitempty"`
	Digest     string             `json:"digest,omitempty"`
}

func Serve(ctx context.Context, cwd string, in io.Reader, out io.Writer) error {
	frame, err := readFrame(in)
	if err != nil {
		return err
	}
	var req Request
	if err := decodeStrict(frame, &req); err != nil {
		return fmt.Errorf("fsops: malformed helper request: %w", err)
	}
	if err := validateRequest(req); err != nil {
		return err
	}
	root, err := Open(cwd)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }() // per-request root; close error cannot replace the operation result
	response := Response{Version: ProtocolVersion, Kind: KindForOperation(req.Operation)}
	switch req.Operation {
	case "read":
		err = requireEOF(in)
		if err != nil {
			break
		}
		reader, info, readErr := root.Read(ctx, req.Path, sandbox.ReadOptions{MaxBytes: req.MaxBytes})
		if readErr != nil {
			setResponseError(&response, readErr)
			break
		}
		defer func() { _ = reader.Close() }() // read handle; body was already streamed
		response.Info = info
		response.BodyLength = min(info.Size, req.MaxBytes)
		if info.Size > req.MaxBytes {
			response.ErrorCode = ErrorCodeReadLimit
		}
		payload, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return fmt.Errorf("fsops: encode read response: %w", marshalErr)
		}
		if err := writeFrame(out, payload); err != nil {
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(reader, response.BodyLength))
		if copyErr != nil {
			return fmt.Errorf("fsops: stream read body: %w", copyErr)
		}
		return nil
	case "write", "upload":
		bodyErr := root.Write(ctx, req.Path, &exactReader{reader: in, remaining: req.BodyLength}, sandbox.WriteOptions{Perm: req.Perm})
		setResponseError(&response, bodyErr)
	case "stat":
		err = requireEOF(in)
		if err != nil {
			break
		}
		response.Info, err = root.Stat(ctx, req.Path)
	case "list":
		err = requireEOF(in)
		if err != nil {
			break
		}
		response.Entries, err = root.List(ctx, req.Path)
	case "managed_skill_target":
		err = requireEOF(in)
		if err != nil {
			break
		}
		response.Digest, response.Managed, err = root.ManagedSkillTargetAt(ctx, path.Dir(req.Path), path.Base(req.Path))
	case "mkdir":
		err = requireEOF(in)
		if err != nil {
			break
		}
		err = root.Mkdir(ctx, req.Path, req.Perm)
	case "remove":
		err = requireEOF(in)
		if err != nil {
			break
		}
		err = root.Remove(ctx, req.Path, req.Recursive)
	case "rename":
		err = requireEOF(in)
		if err != nil {
			break
		}
		err = root.Rename(ctx, req.Path, req.NewPath)
	}
	setResponseError(&response, err)
	payload, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("fsops: encode helper response: %w", err)
	}
	return writeFrame(out, payload)
}

func validateRequest(req Request) error {
	if req.Version != ProtocolVersion {
		return fmt.Errorf("fsops: unsupported helper protocol version %d", req.Version)
	}
	if req.Perm&^0o777 != 0 { // fs.FileMode is unsigned; only stray non-permission bits are invalid
		return errors.New("fsops: invalid permission bits")
	}
	if req.BodyLength < 0 || req.MaxBytes < 0 {
		return errors.New("fsops: invalid size")
	}
	switch req.Operation {
	case "read":
		if req.MaxBytes <= 0 || req.BodyLength != 0 {
			return errors.New("fsops: invalid read request")
		}
	case "write", "upload":
		if req.MaxBytes != 0 {
			return errors.New("fsops: invalid write request")
		}
	case "stat", "list", "managed_skill_target", "mkdir", "remove":
		if req.BodyLength != 0 || req.MaxBytes != 0 || req.NewPath != "" {
			return errors.New("fsops: invalid operation fields")
		}
		if req.Operation == "managed_skill_target" && (req.Path == "" || path.IsAbs(req.Path) || path.Clean(req.Path) != req.Path || req.Path == "." || req.Path == ".." || strings.HasPrefix(req.Path, "../")) {
			return errors.New("fsops: invalid managed skill target path")
		}
	case "rename":
		if req.BodyLength != 0 || req.MaxBytes != 0 || req.NewPath == "" {
			return errors.New("fsops: invalid rename request")
		}
	default:
		return fmt.Errorf("fsops: unsupported helper operation %q", req.Operation)
	}
	return nil
}

type exactReader struct {
	reader    io.Reader
	remaining int64
	checked   bool
}

func (r *exactReader) Read(p []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(p)) > r.remaining {
			p = p[:r.remaining]
		}
		n, err := r.reader.Read(p)
		r.remaining -= int64(n)
		if err == io.EOF && r.remaining != 0 {
			return n, errors.New("fsops: short helper body")
		}
		return n, err
	}
	if r.checked {
		return 0, io.EOF
	}
	r.checked = true
	var extra [1]byte
	n, err := r.reader.Read(extra[:])
	if n != 0 {
		return 0, errors.New("fsops: extra helper body")
	}
	if err != io.EOF {
		return 0, fmt.Errorf("fsops: malformed helper body: %w", err)
	}
	return 0, io.EOF
}

func requireEOF(r io.Reader) error {
	var extra [1]byte
	n, err := r.Read(extra[:])
	if n != 0 {
		return errors.New("fsops: unexpected helper body")
	}
	if err != io.EOF {
		return fmt.Errorf("fsops: malformed helper body: %w", err)
	}
	return nil
}

func setResponseError(response *Response, err error) {
	if err == nil || response.Error != "" {
		return
	}
	response.Error = err.Error()
	response.ErrorCode = classifyErrorCode(err)
}

func EncodeRequest(req Request) ([]byte, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeFrame(&out, payload); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// DecodeResponse reads one helper reply and validates it against the operation
// the caller issued. A response whose kind or field combination does not match
// expected fails closed, so read/stat/list/mutation schemas cannot be confused.
// Read replies must go through DecodeReadResponse so their body bounds are
// checked against the request limit.
func DecodeResponse(r io.Reader, expected string) (Response, error) {
	return decodeResponse(r, expected, nil)
}

// DecodeReadResponse decodes a read reply and additionally enforces that the
// declared body cannot exceed the requested MaxBytes or the reported file size,
// so a malicious helper cannot make the client stream more than it asked for.
func DecodeReadResponse(r io.Reader, maxBytes int64) (Response, error) {
	return decodeResponse(r, KindRead, &maxBytes)
}

func decodeResponse(r io.Reader, expected string, maxBytes *int64) (Response, error) {
	payload, err := readFrame(r)
	if err != nil {
		return Response{}, err
	}
	var response Response
	if err := decodeStrict(payload, &response); err != nil {
		return Response{}, fmt.Errorf("fsops: malformed helper response: %w", err)
	}
	if response.Version != ProtocolVersion {
		return Response{}, fmt.Errorf("fsops: unsupported helper response version %d", response.Version)
	}
	if err := validateResponse(response, expected); err != nil {
		return Response{}, err
	}
	if maxBytes != nil {
		if err := validateReadBounds(response, *maxBytes); err != nil {
			return Response{}, err
		}
	}
	if response.BodyLength == 0 {
		if err := requireEOF(r); err != nil {
			return Response{}, errors.New("fsops: extra helper response frame")
		}
	}
	return response, nil
}

// validateReadBounds rejects a read reply whose declared body is inconsistent
// with the request limit or the reported size. Without read_limit the body must
// exactly equal the file size and fit the limit; with read_limit the body is
// exactly the limit and the file is strictly larger.
func validateReadBounds(response Response, maxBytes int64) error {
	if response.BodyLength > maxBytes {
		return errors.New("fsops: read body exceeds requested limit")
	}
	if response.BodyLength > response.Info.Size {
		return errors.New("fsops: read body exceeds file size")
	}
	if response.ErrorCode == ErrorCodeReadLimit {
		if response.BodyLength != maxBytes || response.Info.Size <= maxBytes {
			return errors.New("fsops: inconsistent read-limit bounds")
		}
		return nil
	}
	if response.BodyLength != response.Info.Size {
		return errors.New("fsops: read body does not match file size")
	}
	return nil
}

func validateResponse(response Response, expected string) error {
	if response.Kind != expected {
		return fmt.Errorf("fsops: helper response kind %q, want %q", response.Kind, expected)
	}
	if response.BodyLength < 0 {
		return errors.New("fsops: invalid response body length")
	}
	if !isKnownErrorCode(response.ErrorCode) {
		return errors.New("fsops: unknown helper error code")
	}
	// An error reply carries a classified non-limit code, a message, and no
	// success payload.
	if response.Error != "" {
		if response.Info != (sandbox.FileInfo{}) || len(response.Entries) != 0 || response.BodyLength != 0 || response.Managed || response.Digest != "" {
			return errors.New("fsops: invalid error response payload")
		}
		if response.ErrorCode == "" || response.ErrorCode == ErrorCodeReadLimit {
			return errors.New("fsops: error reply must carry a non-limit error code")
		}
		return nil
	}
	if expected != KindManagedSkillTarget && (response.Managed || response.Digest != "") {
		return errors.New("fsops: non-managed response carries managed target fields")
	}
	// A success reply carries no code except read_limit, a read-kind partial
	// success delivered with a body.
	if response.ErrorCode == ErrorCodeReadLimit {
		if expected != KindRead || response.BodyLength == 0 {
			return errors.New("fsops: invalid read-limit response")
		}
	} else if response.ErrorCode != "" {
		return errors.New("fsops: success response carries an error code")
	}
	// Disjoint success schemas: each kind owns exactly one payload field.
	switch expected {
	case KindRead:
		if len(response.Entries) != 0 {
			return errors.New("fsops: read response carries directory entries")
		}
	case KindStat:
		if len(response.Entries) != 0 || response.BodyLength != 0 || response.ErrorCode != "" {
			return errors.New("fsops: invalid stat response")
		}
	case KindList:
		if response.Info != (sandbox.FileInfo{}) || response.BodyLength != 0 || response.ErrorCode != "" {
			return errors.New("fsops: invalid list response")
		}
	case KindMutation:
		if response.Info != (sandbox.FileInfo{}) || len(response.Entries) != 0 || response.BodyLength != 0 || response.ErrorCode != "" {
			return errors.New("fsops: invalid mutation response")
		}
	case KindManagedSkillTarget:
		if response.Info != (sandbox.FileInfo{}) || len(response.Entries) != 0 || response.BodyLength != 0 || response.ErrorCode != "" {
			return errors.New("fsops: invalid managed skill target response")
		}
		if response.Managed {
			if !validManagedSkillDigest(response.Digest) {
				return errors.New("fsops: invalid managed skill target digest")
			}
		} else if response.Digest != "" {
			return errors.New("fsops: unmanaged skill target carries digest")
		}
	default:
		return fmt.Errorf("fsops: unknown response kind %q", expected)
	}
	return nil
}

// ResponseError reconstructs the typed error carried by an error reply, wrapping
// the matching sentinel so errors.Is behaves identically to the in-process
// providers. A success reply (including the read_limit partial, whose limit is
// delivered by the streaming reader after its body) yields nil.
func ResponseError(response Response) error {
	if response.Error == "" {
		return nil
	}
	if sentinel := sentinelForCode(response.ErrorCode); sentinel != nil {
		return fmt.Errorf("%w: %s", sentinel, response.Error)
	}
	return errors.New(response.Error)
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("fsops: read frame header: %w", err)
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > maxFrameBytes {
		return nil, fmt.Errorf("fsops: invalid frame size %d", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("fsops: read frame: %w", err)
	}
	return payload, nil
}

func writeFrame(w io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxFrameBytes {
		return fmt.Errorf("fsops: invalid response size %d", len(payload))
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func decodeStrict(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func RunHelper() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return Serve(context.Background(), cwd, os.Stdin, os.Stdout)
}
