package sandbox

import "bytes"

const (
	// MaxExecOutputBytes caps each captured stdout/stderr stream from untrusted execs.
	MaxExecOutputBytes = 10 << 20 // 10 MiB

	execOutputTruncatedMarker = "\n[stella: output truncated, exceeded 10MiB]\n"
)

// CappedOutputBuffer stores command output up to a byte ceiling and discards the rest.
type CappedOutputBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

// NewCappedOutputBuffer returns an io.Writer that captures at most limit bytes.
func NewCappedOutputBuffer(limit int) *CappedOutputBuffer {
	return &CappedOutputBuffer{limit: limit}
}

// NewExecOutputBuffer returns a capped buffer for one exec output stream.
func NewExecOutputBuffer() *CappedOutputBuffer {
	return NewCappedOutputBuffer(MaxExecOutputBytes)
}

func (b *CappedOutputBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	if remaining < len(p) {
		b.truncated = true
	}
	return len(p), nil
}

func (b *CappedOutputBuffer) Bytes() []byte {
	if !b.truncated {
		return b.buf.Bytes()
	}
	out := make([]byte, 0, b.buf.Len()+len(execOutputTruncatedMarker))
	out = append(out, b.buf.Bytes()...)
	out = append(out, execOutputTruncatedMarker...)
	return out
}

func (b *CappedOutputBuffer) String() string {
	return string(b.Bytes())
}

func (b *CappedOutputBuffer) Truncated() bool {
	return b.truncated
}
