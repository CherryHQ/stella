package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	xbergAdapterContractVersion = "v1"
	xbergTimeout                = 60 * time.Second
	xbergStdoutLimit            = 48 << 20
	xbergStderrLimit            = 64 << 10
	xbergCanonicalConfig        = `{"chunking":{"chunker_type":"text","max_characters":1000,"overlap":200,"sizing":"characters","trim":true},"disable_ocr":true,"enable_quality_processing":false,"force_ocr":false,"include_document_structure":true,"output_format":"markdown","pages":{"extract_pages":true,"insert_page_markers":false},"use_cache":false}`
)

type xbergCommandRunner func(context.Context, string, []string) ([]byte, []byte, error)

type XbergCLIParser struct {
	binary, version, profile string
	run                      xbergCommandRunner
}

func NewXbergCLIParser(ctx context.Context, binary string) (*XbergCLIParser, error) {
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("resolve Xberg CLI %q: %w", binary, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute Xberg CLI path: %w", err)
	}
	return newXbergCLIParser(ctx, resolved, runBoundedXbergCommand)
}

func newXbergCLIParser(ctx context.Context, binary string, run xbergCommandRunner) (*XbergCLIParser, error) {
	ctx, cancel := context.WithTimeout(ctx, xbergTimeout)
	defer cancel()
	stdout, _, runErr := run(ctx, binary, []string{"version", "--format", "json"})
	if runErr != nil {
		return nil, fmt.Errorf("probe Xberg CLI version: %w", runErr)
	}
	var envelope struct {
		Version string `json:"version"`
		Result  struct {
			Version string `json:"version"`
		} `json:"result"`
	}
	if err := decodeSingleJSON(stdout, &envelope); err != nil {
		return nil, fmt.Errorf("probe Xberg CLI version JSON: %w", err)
	}
	version := strings.TrimSpace(envelope.Version)
	if version == "" {
		version = strings.TrimSpace(envelope.Result.Version)
	}
	if version == "" {
		return nil, fmt.Errorf("probe Xberg CLI version JSON: version is missing")
	}
	configHash := sha256.Sum256([]byte(xbergCanonicalConfig))
	profile := "xberg-cli-adapter:" + xbergAdapterContractVersion + ";cli=" + version + ";config_sha256=" + hex.EncodeToString(configHash[:])
	return &XbergCLIParser{binary: binary, version: version, profile: profile, run: run}, nil
}

func (p *XbergCLIParser) Profile(mediaType string) (string, error) {
	if mediaType != MediaTypePDF && mediaType != MediaTypeDOCX {
		return "", fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
	return p.profile, nil
}

func (p *XbergCLIParser) Parse(ctx context.Context, path, mediaType string) ([]ParsedChunk, error) {
	if _, err := p.Profile(mediaType); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve Xberg input path: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, xbergTimeout)
	defer cancel()
	args := []string{"extract", absolute, "--no-config-discovery", "--config-json", xbergCanonicalConfig, "--format", "json"}
	stdout, _, runErr := p.run(ctx, p.binary, args)
	if runErr != nil {
		return nil, fmt.Errorf("run Xberg extraction: %w", runErr)
	}
	var envelope struct {
		Result struct {
			Chunks []struct {
				Content  string `json:"content"`
				Metadata struct {
					ByteStart   *int     `json:"byte_start"`
					ByteEnd     *int     `json:"byte_end"`
					FirstPage   *uint32  `json:"first_page"`
					LastPage    *uint32  `json:"last_page"`
					HeadingPath []string `json:"heading_path"`
				} `json:"metadata"`
			} `json:"chunks"`
		} `json:"result"`
	}
	if err := decodeSingleJSON(stdout, &envelope); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidParserData, err)
	}
	if len(envelope.Result.Chunks) == 0 {
		return nil, ErrNoExtractedText
	}
	chunks := make([]ParsedChunk, len(envelope.Result.Chunks))
	for i, chunk := range envelope.Result.Chunks {
		if chunk.Metadata.ByteStart == nil || chunk.Metadata.ByteEnd == nil || *chunk.Metadata.ByteEnd <= *chunk.Metadata.ByteStart {
			return nil, fmt.Errorf("%w: Xberg chunk %d has missing or invalid byte offsets", ErrInvalidParserData, i)
		}
		chunks[i] = ParsedChunk{Content: chunk.Content, Locator: ChunkLocator{
			ByteStart: *chunk.Metadata.ByteStart, ByteEnd: *chunk.Metadata.ByteEnd,
			FirstPage: chunk.Metadata.FirstPage, LastPage: chunk.Metadata.LastPage,
			HeadingPath: chunk.Metadata.HeadingPath,
		}}
	}
	return chunks, nil
}

type cappedBuffer struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		b.exceeded = true
		allowed := b.max - b.Len()
		if allowed > 0 {
			_, _ = b.Buffer.Write(p[:allowed])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func runBoundedXbergCommand(ctx context.Context, binary string, args []string) ([]byte, []byte, error) {
	stdout := &cappedBuffer{max: xbergStdoutLimit}
	stderr := &cappedBuffer{max: xbergStderrLimit}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.exceeded {
		return nil, stderr.Bytes(), fmt.Errorf("%w: Xberg stdout exceeds %d bytes", ErrParseResultLimit, xbergStdoutLimit)
	}
	if stderr.exceeded {
		return nil, stderr.Bytes(), fmt.Errorf("xberg stderr exceeds %d bytes", xbergStderrLimit)
	}
	if err != nil {
		return nil, stderr.Bytes(), err
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

func decodeSingleJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}
