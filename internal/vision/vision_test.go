package vision

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// pngBytes builds a tiny valid PNG. w varies the content so tests can produce
// two images that differ in bytes.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.RGBA{R: uint8(x), G: 100, B: 50, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// installXbergShim writes a fake Xberg CLI under a fresh STELLA_HOME so the
// fallback path runs without the real binary. It returns the text the shim
// prints.
func installXbergShim(t *testing.T, output string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	shim := filepath.Join(pkgsandbox.MiseShimsDir(stellaHome), "xberg")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatalf("create shim directory: %v", err)
	}
	script := "#!/bin/sh\n[ \"$1\" = extract ] || exit 1\nprintf %s '" + output + "'\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write Xberg shim: %v", err)
	}
	return output
}

// disableXberg points STELLA_HOME at an empty directory and empties PATH so
// neither the managed shim nor a host-installed xberg can be found.
func disableXberg(t *testing.T) {
	t.Helper()
	t.Setenv("STELLA_HOME", t.TempDir())
	t.Setenv("PATH", "")
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
}

// textStream returns a StreamBuilder whose model answers with text, plus a
// counter of how many times the model was actually called.
func textStream(text string) (StreamBuilder, *atomic.Int64, *ai.Context) {
	var calls atomic.Int64
	var seen ai.Context
	build := func(_, _, _ string) (providers.StreamFunc, error) {
		return func(_ context.Context, _ ai.Model, aiCtx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
			calls.Add(1)
			seen = aiCtx
			s := providers.NewChannelEventStream(4)
			go func() {
				s.Emit(ai.EventTextDelta{Text: text})
				s.Emit(ai.EventStop{Reason: ai.StopReasonStop})
				s.Finish(nil)
			}()
			return s, nil
		}, nil
	}
	return build, &calls, &seen
}

// failingStream returns a StreamBuilder whose model always errors.
func failingStream() (StreamBuilder, *atomic.Int64) {
	var calls atomic.Int64
	build := func(_, _, _ string) (providers.StreamFunc, error) {
		return func(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
			calls.Add(1)
			return nil, errors.New("provider exploded")
		}, nil
	}
	return build, &calls
}

func testOptions(build StreamBuilder) Options {
	return Options{
		Model:  ai.Model{ID: "vlm-1", Name: "vlm-1", API: "openai-completions", Provider: "openai"},
		APIKey: "k",
		Build:  build,
	}
}

func TestUnderstandUsesVisionModel(t *testing.T) {
	build, calls, seen := textStream("## Text\nhello\n\n## Scene\nA screenshot.")
	svc := New(testOptions(build))
	if !svc.ModelConfigured() {
		t.Fatal("expected a configured vision model")
	}

	res, err := svc.Understand(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if res.Source != SourceModel {
		t.Errorf("Source = %q, want %q", res.Source, SourceModel)
	}
	if !strings.Contains(res.Text, "hello") {
		t.Errorf("Text = %q, want it to carry the model output", res.Text)
	}
	if calls.Load() != 1 {
		t.Errorf("model calls = %d, want 1", calls.Load())
	}

	// The image must actually reach the model, as an image block.
	user, ok := seen.Messages[0].(ai.UserMessage)
	if !ok {
		t.Fatalf("first message = %T, want ai.UserMessage", seen.Messages[0])
	}
	blocks, ok := user.Content.([]ai.ContentBlock)
	if !ok {
		t.Fatalf("user content = %T, want []ai.ContentBlock", user.Content)
	}
	if !ai.HasImage(blocks) {
		t.Error("expected the request to carry an image block")
	}
	if seen.System == "" {
		t.Error("expected the extraction system prompt to be set")
	}
}

func TestUnderstandFallsBackToXbergWhenModelFails(t *testing.T) {
	want := installXbergShim(t, "extracted by xberg")
	build, modelCalls := failingStream()
	svc := New(testOptions(build))

	res, err := svc.Understand(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if res.Source != SourceXberg {
		t.Errorf("Source = %q, want %q", res.Source, SourceXberg)
	}
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
	if modelCalls.Load() != 1 {
		t.Errorf("model calls = %d, want the model to be tried once first", modelCalls.Load())
	}
}

func TestUnderstandWithoutModelGoesStraightToXberg(t *testing.T) {
	want := installXbergShim(t, "no model, xberg only")
	svc := New(Options{})
	if svc.ModelConfigured() {
		t.Fatal("expected no vision model without options")
	}

	res, err := svc.Understand(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
	if err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if res.Source != SourceXberg || res.Text != want {
		t.Errorf("got %+v, want xberg text %q", res, want)
	}
}

func TestUnderstandErrorsWhenModelAndXbergBothFail(t *testing.T) {
	disableXberg(t)
	build, _ := failingStream()
	svc := New(testOptions(build))

	_, err := svc.Understand(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
	if err == nil {
		t.Fatal("expected an error when neither backend can render the image")
	}
	// The message must name both failures so an operator can tell them apart.
	if !strings.Contains(err.Error(), "provider exploded") {
		t.Errorf("error %q should mention the model failure", err)
	}
	if !strings.Contains(err.Error(), "xberg") {
		t.Errorf("error %q should mention the xberg failure", err)
	}
}

func TestUnderstandRejectsOversizedImageBeforeAnyBackend(t *testing.T) {
	build, calls := failingStream()
	svc := New(testOptions(build))

	_, err := svc.Understand(context.Background(), Request{Data: pngHeaderWithDims(8000, 8000), MimeType: "image/png"})
	if err == nil {
		t.Fatal("expected the decode budget to reject a 64MP image")
	}
	if calls.Load() != 0 {
		t.Errorf("model calls = %d, want the budget check to run before any backend", calls.Load())
	}
}

func TestUnderstandMemoizesByImageContent(t *testing.T) {
	build, calls, _ := textStream("rendered")
	svc := New(testOptions(build))
	same := pngBytes(t, 8, 8)
	other := pngBytes(t, 9, 9)

	for range 3 {
		if _, err := svc.Understand(context.Background(), Request{Data: same, MimeType: "image/png"}); err != nil {
			t.Fatalf("Understand: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want the same image rendered once", calls.Load())
	}

	if _, err := svc.Understand(context.Background(), Request{Data: other, MimeType: "image/png"}); err != nil {
		t.Fatalf("Understand: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("model calls = %d, want a different image to be rendered again", calls.Load())
	}
}

func TestUnderstandMemoizesFailures(t *testing.T) {
	disableXberg(t)
	build, calls := failingStream()
	svc := New(testOptions(build))
	data := pngBytes(t, 8, 8)

	for range 3 {
		if _, err := svc.Understand(context.Background(), Request{Data: data, MimeType: "image/png"}); err == nil {
			t.Fatal("expected an error")
		}
	}
	// Retrying a hopeless image every turn would re-pay the model timeout.
	if calls.Load() != 1 {
		t.Errorf("model calls = %d, want a cached failure after the first attempt", calls.Load())
	}
}

func TestNilServiceFallsBackToXberg(t *testing.T) {
	want := installXbergShim(t, "nil service still extracts")
	var svc *Service

	res, err := svc.Understand(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
	if err != nil {
		t.Fatalf("Understand on nil service: %v", err)
	}
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

func TestNewFromSnapshotWithoutVisionTier(t *testing.T) {
	build, _, _ := textStream("unused")
	snap := &config.Snapshot{
		Provider:  "openai",
		Model:     "openai/gpt-4o-mini",
		Providers: map[string]config.ProviderCreds{"openai": {Type: "openai-completions", APIKey: "k"}},
	}
	if svc := NewFromSnapshot(snap, build); svc.ModelConfigured() {
		t.Error("a deployment with no vision setting must have no vision model")
	}

	snap.ModelVision = "openai/gpt-4o"
	if svc := NewFromSnapshot(snap, build); !svc.ModelConfigured() {
		t.Error("a deployment with a vision setting must have a vision model")
	}
}

func TestExtractWithXbergUsesManagedShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	shim := filepath.Join(pkgsandbox.MiseShimsDir(stellaHome), "xberg")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatalf("create shim directory: %v", err)
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\n[ \"$1\" = extract ] || exit 1\nprintf %s \"$PWD\"\n"), 0o755); err != nil {
		t.Fatalf("write Xberg shim: %v", err)
	}
	inputDir := t.TempDir()

	got, err := ExtractWithXberg(context.Background(), filepath.Join(inputDir, "document.pdf"))
	if err != nil {
		t.Fatalf("ExtractWithXberg() error: %v", err)
	}
	want, err := filepath.EvalSymlinks(inputDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Errorf("ExtractWithXberg() cwd = %q, want %q", got, want)
	}
}

func TestValidateBudgetRejectsPixelBomb(t *testing.T) {
	// A header claiming a huge canvas must be rejected from the IHDR alone,
	// without allocating the full pixel buffer the bomb would expand to.
	header := pngHeaderWithDims(8000, 8000) // 64MP > maxImagePixels
	if _, err := ValidateBudget(header); err == nil {
		t.Fatal("expected oversized image (64MP) to be rejected before decode")
	}
}

// pngHeaderWithDims builds a minimal PNG (signature + IHDR chunk only) declaring
// the given dimensions. image.DecodeConfig reads the size from IHDR without
// decoding pixels, so this exercises the header-based budget check cheaply.
func pngHeaderWithDims(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // color type: RGBA truecolor (DecodeConfig returns after IHDR)
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, 13)
	buf.Write(length)
	chunk := append([]byte("IHDR"), ihdr...)
	buf.Write(chunk)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(chunk))
	buf.Write(crc)
	return buf.Bytes()
}
