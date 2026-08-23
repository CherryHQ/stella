package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
	localplugin "github.com/CherryHQ/stella/plugins/sandbox/local"
	noneplugin "github.com/CherryHQ/stella/plugins/sandbox/none"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

type readErrorFiles struct {
	pkgsandbox.FileAccess
	err error
}

func (f readErrorFiles) ReadFile(string) ([]byte, error) { return nil, f.err }

func newTestReadTool(t *testing.T, projectRoot string) pkgtools.Tool {
	t.Helper()
	policy := pkgsandbox.Policy{
		Filesystem: pkgsandbox.FilesystemPolicy{
			WorkingDir: pkgsandbox.MountWorkspace,
			Mounts:     []pkgsandbox.Mount{{SandboxPath: pkgsandbox.MountWorkspace, Access: pkgsandbox.MountReadWrite}},
		},
		Network: pkgsandbox.NetworkPolicy{Mode: pkgsandbox.NetworkAllowAll},
	}
	session, err := noneplugin.NewFactoryWithMountSources(map[string]string{pkgsandbox.MountWorkspace: projectRoot}, noneplugin.Config{}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Fatalf("create test sandbox Session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return newReadTool(session)
}

func TestReadImageVisionReturnsImageBlock(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "pic.png"), 10, 10)
	tool := newTestReadTool(t, dir)

	blocks, err := pkgtools.ExecuteToolContent(context.Background(), tool, map[string]any{"path": "pic.png"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	if !ai.HasImage(blocks) {
		t.Fatalf("expected an image block, got %#v", blocks)
	}
	var img ai.ImageContent
	for _, b := range blocks {
		if ic, ok := b.(ai.ImageContent); ok {
			img = ic
		}
	}
	if img.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", img.MimeType)
	}
	if _, err := base64.StdEncoding.DecodeString(img.Data); err != nil {
		t.Errorf("image data is not valid base64: %v", err)
	}
}

func TestReadImageResizesLargeImage(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "big.png"), 3000, 100)
	tool := newTestReadTool(t, dir)

	blocks, err := pkgtools.ExecuteToolContent(context.Background(), tool, map[string]any{"path": "big.png"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	var img ai.ImageContent
	for _, b := range blocks {
		if ic, ok := b.(ai.ImageContent); ok {
			img = ic
		}
	}
	raw, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.Width > vision.MaxImageDim || cfg.Height > vision.MaxImageDim {
		t.Errorf("image not resized: %dx%d exceeds %d", cfg.Width, cfg.Height, vision.MaxImageDim)
	}
}

func TestReadImageCanonicalResultKeepsOriginalForTransform(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	writePNG(t, path, 10, 10)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := pkgtools.ExecuteToolContent(pkgtools.WithImageResultMode(context.Background(), pkgtools.ImageResultCanonical), newTestReadTool(t, dir), map[string]any{"path": "pic.png"})
	if err != nil || !ai.HasImage(blocks) {
		t.Fatalf("canonical read = %#v, %v", blocks, err)
	}
	for _, block := range blocks {
		if img, ok := block.(ai.ImageContent); ok {
			got, _ := base64.StdEncoding.DecodeString(img.Data)
			if !bytes.Equal(got, original) {
				t.Fatal("canonical read changed original bytes")
			}
		}
	}
}

func TestReadTooLargeErrorSuggestsExecutableBashRange(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_BYTES", "")
	const (
		publicPath = "/app/input file's.csv"
		hostPath   = "/private/host/eval/input.csv"
		size       = int64(51_066_691)
		limit      = int64(33_554_432)
	)
	host := &stubHost{
		workingDir: "/app",
		resolvePath: func(string) (string, error) {
			return hostPath, nil
		},
		files: readErrorFiles{err: &pkgsandbox.FileTooLargeError{Size: size, Limit: limit}},
	}

	_, err := newReadTool(host).Execute(context.Background(), map[string]any{"path": publicPath})
	if err == nil {
		t.Fatal("expected oversized read to fail")
	}
	message := err.Error()
	outputByteLimit := pkgtools.OutputByteLimit()
	sliceLines := max(outputByteLimit/conservativeReadSliceBytesPerLine, 1)
	outputLimitKB := max((outputByteLimit+bytesPerKiB-1)/bytesPerKiB, 1)
	for _, want := range []string{
		publicPath,
		"51066691 bytes",
		"33554432-byte read cap",
		fmt.Sprintf("capped at ~%d KB per call", outputLimitKB),
		fmt.Sprintf("a %d-line slice from the beginning, not the whole file", sliceLines),
		fmt.Sprintf("bash(command=%q)", fmt.Sprintf("sed -n '1,%dp' < %s", sliceLines, shellQuoteToolPath(publicPath))),
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not contain %q", message, want)
		}
	}
	if strings.Contains(message, hostPath) {
		t.Fatalf("error leaked resolved host path: %q", message)
	}
}

func TestShellQuoteLeadingVariablePathQuotesBackslashSuffix(t *testing.T) {
	got := shellQuoteLeadingVariablePath("$HOME", `\foo`)
	want := `"$HOME"'\foo'`
	if got != want {
		t.Fatalf("quoted path = %q, want %q", got, want)
	}
}

func TestOversizedReadSliceCommandExecutesForAgentPaths(t *testing.T) {
	t.Setenv("STELLA_TOOL_MAX_BYTES", "500")
	root := t.TempDir()
	home := filepath.Join(root, "home dir")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		toolPath string
		filePath string
	}{
		{name: "ordinary", toolPath: filepath.Join(root, "plain.csv"), filePath: filepath.Join(root, "plain.csv")},
		{name: "space", toolPath: filepath.Join(root, "input file.csv"), filePath: filepath.Join(root, "input file.csv")},
		{name: "single quote", toolPath: filepath.Join(root, "input'file.csv"), filePath: filepath.Join(root, "input'file.csv")},
		{name: "leading HOME", toolPath: "$HOME/home file's.csv", filePath: filepath.Join(home, "home file's.csv")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(tt.filePath, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			command, sliceLines, _ := oversizedReadSliceCommand(tt.toolPath)
			if sliceLines != 2 {
				t.Fatalf("slice lines = %d, want 2", sliceLines)
			}
			cmd := exec.Command("sh", "-c", command)
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("command %q failed: %v\n%s", command, err, output)
			}
			if got, want := string(output), "line1\nline2\n"; got != want {
				t.Fatalf("command %q output = %q, want %q", command, got, want)
			}
		})
	}
}

func TestReadTextFileReturnsTextBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := newTestReadTool(t, dir)

	blocks, err := pkgtools.ExecuteToolContent(context.Background(), tool, map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	if ai.HasImage(blocks) {
		t.Fatal("text file must not produce an image block")
	}
	if got := ai.FlattenText(blocks); !strings.Contains(got, "line1") || !strings.Contains(got, "line2") {
		t.Errorf("unexpected text output: %q", got)
	}
}

func TestReadToolUsesSessionProcessViewBoundary(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "inside.txt"), []byte("inside workspace\n"), 0o644); err != nil {
		t.Fatalf("write inside: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside workspace\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	policy := pkgsandbox.Policy{
		Filesystem: pkgsandbox.FilesystemPolicy{
			WorkingDir: pkgsandbox.MountWorkspace,
			Mounts:     []pkgsandbox.Mount{{SandboxPath: pkgsandbox.MountWorkspace, Access: pkgsandbox.MountReadWrite}},
		},
		Network: pkgsandbox.NetworkPolicy{Mode: pkgsandbox.NetworkAllowAll},
	}
	session, err := localplugin.NewFactoryWithMountSources(map[string]string{pkgsandbox.MountWorkspace: workspace}, localplugin.Config{}).CreateSession(context.Background(), policy)
	if err != nil {
		t.Skipf("local sandbox unavailable: %v", err)
	}
	defer session.Close() //nolint:errcheck

	tool := newReadTool(session)
	out, err := tool.Execute(context.Background(), map[string]any{"path": "inside.txt"})
	if err != nil {
		t.Fatalf("read inside workspace: %v", err)
	}
	if !strings.Contains(out, "inside workspace") {
		t.Fatalf("read inside output = %q", out)
	}

	out, err = tool.Execute(context.Background(), map[string]any{"path": outside})
	if err == nil {
		t.Fatalf("expected outside absolute path to be rejected, got output %q", out)
	}
}
