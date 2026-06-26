package document

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

type cliExtractor struct{}

func (cliExtractor) ExtractFile(ctx context.Context, path string, opts Options) (*Result, error) {
	bin, err := exec.LookPath("kreuzberg")
	if err != nil {
		return nil, WrapUnavailable(err)
	}
	cctx, cancel := WithTimeout(ctx, opts.Timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "extract", path).Output()
	if err != nil {
		return nil, fmt.Errorf("extract document: %w", err)
	}
	return NormalizeResult(&Result{Content: string(out)})
}

func (e cliExtractor) ExtractBytes(ctx context.Context, data []byte, mime string, opts Options) (*Result, error) {
	f, err := os.CreateTemp("", "stella-document-*")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	result, err := e.ExtractFile(ctx, path, opts)
	if result != nil {
		result.MimeType = mime
	}
	return result, err
}
