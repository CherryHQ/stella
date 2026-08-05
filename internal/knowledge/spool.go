package knowledge

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const spoolCopyBufferSize = 64 << 10

// spoolBudget bounds upload concurrency and the aggregate bytes currently
// materialized in acquisition temp files on one Stella node.
type spoolBudget struct {
	mu            sync.Mutex
	maxConcurrent int
	maxBytes      int64
	active        int
	bytes         int64
}

func newSpoolBudget(maxConcurrent int, maxBytes int64) (*spoolBudget, error) {
	if maxConcurrent < 1 {
		return nil, fmt.Errorf("knowledge max concurrent uploads must be positive")
	}
	if maxBytes < MaxFileBytes {
		return nil, fmt.Errorf("knowledge spool budget must be at least %d bytes", MaxFileBytes)
	}
	return &spoolBudget{maxConcurrent: maxConcurrent, maxBytes: maxBytes}, nil
}

func (b *spoolBudget) begin() (*spoolReservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active >= b.maxConcurrent {
		return nil, ErrSpoolCapacity
	}
	b.active++
	return &spoolReservation{budget: b}, nil
}

type spoolReservation struct {
	budget   *spoolBudget
	reserved int64
	released bool
}

func (r *spoolReservation) add(bytes int64) error {
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	if r.released {
		return ErrSpoolCapacity
	}
	if r.budget.bytes+bytes > r.budget.maxBytes {
		return ErrSpoolCapacity
	}
	r.budget.bytes += bytes
	r.reserved += bytes
	return nil
}

func (r *spoolReservation) release() {
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	if r.released {
		return
	}
	r.budget.bytes -= r.reserved
	r.budget.active--
	r.released = true
}

type preparedUpload struct {
	fileName    string
	mediaType   string
	path        string
	sizeBytes   int64
	rawSHA256   []byte
	reservation *spoolReservation
}

func (p *preparedUpload) close() {
	if p == nil {
		return
	}
	if p.path != "" {
		_ = os.Remove(p.path)
	}
	if p.reservation != nil {
		p.reservation.release()
	}
}

func prepareUpload(
	ctx context.Context,
	tempDir string,
	budget *spoolBudget,
	fileName string,
	source io.Reader,
) (*preparedUpload, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: content is missing", ErrInvalidFile)
	}
	safeName, mediaType, err := validateUploadName(fileName)
	if err != nil {
		return nil, err
	}
	reservation, err := budget.begin()
	if err != nil {
		return nil, err
	}
	prepared := &preparedUpload{fileName: safeName, mediaType: mediaType, reservation: reservation}
	succeeded := false
	defer func() {
		if !succeeded {
			prepared.close()
		}
	}()

	temp, err := os.CreateTemp(tempDir, ".stella-knowledge-upload-*")
	if err != nil {
		return nil, fmt.Errorf("create upload spool: %w", err)
	}
	prepared.path = temp.Name()
	// os.CreateTemp uses 0600, but keep the server-only contract explicit.
	if err = temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return nil, fmt.Errorf("restrict upload spool: %w", err)
	}

	hash := sha256.New()
	buffer := make([]byte, spoolCopyBufferSize)
	for {
		if err = ctx.Err(); err != nil {
			_ = temp.Close()
			return nil, err
		}
		var read int
		read, err = source.Read(buffer)
		if read > 0 {
			if prepared.sizeBytes+int64(read) > MaxFileBytes {
				_ = temp.Close()
				return nil, fmt.Errorf("%w: maximum is %d bytes", ErrFileTooLarge, MaxFileBytes)
			}
			if reserveErr := reservation.add(int64(read)); reserveErr != nil {
				_ = temp.Close()
				return nil, reserveErr
			}
			if _, writeErr := temp.Write(buffer[:read]); writeErr != nil {
				_ = temp.Close()
				return nil, fmt.Errorf("write upload spool: %w", writeErr)
			}
			_, _ = hash.Write(buffer[:read])
			prepared.sizeBytes += int64(read)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = temp.Close()
			return nil, fmt.Errorf("read knowledge upload: %w", err)
		}
		if read == 0 {
			_ = temp.Close()
			return nil, fmt.Errorf("read knowledge upload: reader made no progress")
		}
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return nil, fmt.Errorf("sync upload spool: %w", err)
	}
	if err = temp.Close(); err != nil {
		return nil, fmt.Errorf("close upload spool: %w", err)
	}
	if err = validateUploadFile(prepared.path, prepared.mediaType); err != nil {
		return nil, err
	}
	prepared.rawSHA256 = hash.Sum(nil)
	succeeded = true
	return prepared, nil
}
