package vision

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
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
	"github.com/CherryHQ/stella/resources/binaries"
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

// installXbergBinary writes a fake Xberg CLI under a fresh STELLA_HOME so the
// fallback path runs without the real binary. It returns the text the fake
// prints.
func installXbergBinary(t *testing.T, output string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	bin := filepath.Join(binaries.BinDir(stellaHome), "xberg")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatalf("create bin directory: %v", err)
	}
	script := "#!/bin/sh\n[ \"$1\" = extract ] || exit 1\nprintf %s '" + output + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write Xberg binary: %v", err)
	}
	return output
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

func testOptions(build StreamBuilder) Options {
	return Options{
		Model:  ai.Model{ID: "vlm-1", Name: "vlm-1", API: "openai-completions", Provider: "openai"},
		APIKey: "k",
		Build:  build,
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

func TestCanDescribeImagesCapabilityGate(t *testing.T) {
	build, _, _ := textStream("unused")
	for name, tt := range map[string]struct {
		input []string
		want  bool
	}{
		"undeclared input passes": {input: nil, want: true},
		"image input passes":      {input: []string{"text", "image"}, want: true},
		"text-only fails closed":  {input: []string{"text"}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			opts := testOptions(build)
			opts.Model.Input = tt.input
			if got := New(opts).CanDescribeImages(); got != tt.want {
				t.Errorf("CanDescribeImages() = %t, want %t (Input=%v)", got, tt.want, tt.input)
			}
		})
	}
	if New(Options{}).CanDescribeImages() {
		t.Error("unconfigured service must not describe images")
	}
	var nilSvc *Service
	if nilSvc.CanDescribeImages() {
		t.Error("nil service must not describe images")
	}
}

func TestDescribeRejectsTextOnlyVisionModel(t *testing.T) {
	build, calls, _ := textStream("never returned")
	opts := testOptions(build)
	opts.Model.Input = []string{"text"}
	_, err := New(opts).Describe(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"}, "what is this")
	if !errors.Is(err, ErrNoVisionModel) {
		t.Fatalf("err = %v, want ErrNoVisionModel", err)
	}
	if !strings.Contains(err.Error(), "text-only") {
		t.Fatalf("error must name the text-only declaration, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0: a text-only model must never receive image bytes", calls.Load())
	}
}

func TestBaselineSkipsTextOnlyVisionModel(t *testing.T) {
	want := installXbergBinary(t, "xberg only text")
	build, calls, _ := textStream("## Text\nmodel\n\n## Scene\nModel scene text.")
	opts := testOptions(build)
	opts.Model.Input = []string{"text"}
	result, err := New(opts).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0: a text-only model must never receive image bytes", calls.Load())
	}
	if !strings.Contains(result.Text, want) {
		t.Fatalf("baseline = %q, want Xberg fallback containing %q", result.Text, want)
	}
}

func TestNewFromSnapshotCarriesVisionModelInput(t *testing.T) {
	build, _, _ := textStream("unused")
	snap := &config.Snapshot{
		Provider:    "openai",
		Model:       "openai/gpt-4o-mini",
		ModelVision: "openai/text-only",
		Providers:   map[string]config.ProviderCreds{"openai": {Type: "openai-completions", APIKey: "k"}},
		ModelInputs: map[config.ModelKey][]string{{Provider: "openai", Model: "text-only"}: {"text"}},
	}
	svc := NewFromSnapshot(snap, build)
	if !svc.ModelConfigured() {
		t.Fatal("service with a resolvable vision tier must be configured")
	}
	if svc.CanDescribeImages() {
		t.Error("text-only vision tier must not describe images")
	}
}

func TestBaselineUsesValidatedModelContract(t *testing.T) {
	build, _, _ := textStream("## Text\nhello\n\n## Scene\nA tiny screenshot with one word.")
	result, err := New(testOptions(build)).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if err := ai.ValidateImageBaselineText(result.Text); err != nil {
		t.Errorf("model baseline contract: %v", err)
	}
}

func TestBaselineContractGolden(t *testing.T) {
	golden, err := os.ReadFile("testdata/baseline-v1.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	text := strings.TrimSpace(string(golden))
	if err := ai.ValidateImageBaselineText(text); err != nil {
		t.Fatalf("golden contract: %v", err)
	}
	build, _, _ := textStream(text)
	result, err := New(testOptions(build)).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if result.Text != text {
		t.Fatalf("baseline = %q, want golden %q", result.Text, text)
	}
}

func TestBaselineFallsBackForMalformedOrTruncatedModelOutput(t *testing.T) {
	for _, tt := range []struct {
		name   string
		text   string
		reason ai.StopReason
	}{
		{name: "malformed", text: "unstructured answer", reason: ai.StopReasonStop},
		{name: "truncated", text: "## Text\npartial", reason: ai.StopReasonLength},
		{name: "missing stop", text: "## Text\na\n\n## Scene\nb", reason: ai.StopReasonUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			want := installXbergBinary(t, "xberg visible text")
			build := func(_, _, _ string) (providers.StreamFunc, error) {
				return func(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
					s := providers.NewChannelEventStream(3)
					go func() {
						s.Emit(ai.EventTextDelta{Text: tt.text})
						if tt.reason != ai.StopReasonUnknown {
							s.Emit(ai.EventStop{Reason: tt.reason})
						}
						s.Finish(nil)
					}()
					return s, nil
				}, nil
			}
			result, err := New(testOptions(build)).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
			if err != nil {
				t.Fatalf("Baseline: %v", err)
			}
			if !strings.Contains(result.Text, want) {
				t.Fatalf("fallback result = %+v, want normalized Xberg text %q", result, want)
			}
			if err := ai.ValidateImageBaselineText(result.Text); err != nil {
				t.Fatalf("normalized Xberg contract: %v", err)
			}
		})
	}
}

func TestNormalizeXbergBaselineKeepsContractForOCRHeadings(t *testing.T) {
	got := NormalizeXbergBaseline("heading\n\n## Scene\nvisible OCR text")
	if err := ai.ValidateImageBaselineText(got); err != nil {
		t.Fatalf("normalized Xberg baseline invalid: %v", err)
	}
	if !strings.Contains(got, "# # Scene") {
		t.Fatalf("normalized Xberg baseline retained delimiter in OCR body: %q", got)
	}
}

func TestBaselineWithoutModelUsesXberg(t *testing.T) {
	want := installXbergBinary(t, "OCR only")
	result, err := New(Options{}).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if !strings.Contains(result.Text, want) {
		t.Errorf("baseline = %+v", result)
	}
}

func TestBaselineRejectsMIMEMismatch(t *testing.T) {
	_, err := New(Options{}).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/jpeg"})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Baseline MIME mismatch error = %v", err)
	}
}

func TestPrepareRendererPayloadPreservesSupportedPayloadsBelowCeiling(t *testing.T) {
	for _, mime := range []string{"image/png", "image/jpeg", "image/gif"} {
		t.Run(mime, func(t *testing.T) {
			// Wide enough that a dimension-based rule would have resized it, small
			// enough to stay under the hard payload ceiling.
			data := encodedImage(t, mime, 2001, 2)
			cfg, detectedMIME, err := ValidateImage(data, mime)
			if err != nil {
				t.Fatalf("ValidateImage: %v", err)
			}
			prepared, preparedMIME, err := PrepareRendererPayloadContext(context.Background(), data, cfg, detectedMIME)
			if err != nil {
				t.Fatalf("PrepareRendererPayloadContext: %v", err)
			}
			if !bytes.Equal(prepared, data) || preparedMIME != detectedMIME {
				t.Error("baseline preparation changed an image below the hard payload ceiling")
			}
		})
	}
}

func TestPrepareRendererPayloadContextHonorsCancellation(t *testing.T) {
	data := pngBytes(t, 8, 8)
	cfg, mime, err := ValidateImage(data, "image/png")
	if err != nil {
		t.Fatalf("ValidateImage: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := PrepareRendererPayloadContext(ctx, data, cfg, mime); !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareRendererPayloadContext cancellation error = %v, want context.Canceled", err)
	}
}

func encodedImage(t *testing.T, mime string, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 100, G: 50, B: 25, A: 255})
	var buf bytes.Buffer
	var err error
	switch mime {
	case "image/jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "image/gif":
		err = gif.Encode(&buf, img, nil)
	default:
		err = png.Encode(&buf, img)
	}
	if err != nil {
		t.Fatalf("encode %s: %v", mime, err)
	}
	return buf.Bytes()
}

func TestExtractWithXbergUsesEmbeddedRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	bin := filepath.Join(binaries.BinDir(stellaHome), "xberg")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatalf("create bin directory: %v", err)
	}
	script := "#!/bin/sh\n" +
		"[ \"$1\" = extract ] || exit 1\n" +
		"printf 'pwd=%s\\n' \"$PWD\"\n" +
		"for a in \"$@\"; do [ \"$a\" = --no-config-discovery ] && printf 'noconfig\\n'; done\n" +
		"printf 'secret=%s\\n' \"${STELLA_TEST_SECRET:-}\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write Xberg binary: %v", err)
	}
	// A credential the daemon would hold. Xberg must not observe it.
	t.Setenv("STELLA_TEST_SECRET", "provider-key")
	inputDir := t.TempDir()

	got, err := ExtractWithXberg(context.Background(), filepath.Join(inputDir, "document.pdf"))
	if err != nil {
		t.Fatalf("ExtractWithXberg() error: %v", err)
	}
	if !strings.Contains(got, "noconfig") {
		t.Error("ExtractWithXberg() did not pass --no-config-discovery")
	}
	if !strings.Contains(got, "secret=\n") && !strings.HasSuffix(got, "secret=") {
		t.Errorf("ExtractWithXberg() leaked the daemon environment: %q", got)
	}
	// The working directory anchors config discovery, so it must be a directory
	// Stella owns — the runtime's own bin dir — not the staging dir, which on a
	// shared host is a world-writable temp root any local user could plant
	// xberg.toml in.
	var pwd string
	for line := range strings.SplitSeq(got, "\n") {
		if rest, ok := strings.CutPrefix(line, "pwd="); ok {
			pwd = rest
		}
	}
	pwd, err = filepath.EvalSymlinks(pwd)
	if err != nil {
		t.Fatalf("EvalSymlinks(pwd): %v", err)
	}
	// macOS hands out /var/folders temp dirs that symlink into /private/var, and
	// the shell may report either spelling; compare the resolved paths.
	want, err := filepath.EvalSymlinks(binaries.BinDir(stellaHome))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if pwd != want {
		t.Errorf("ExtractWithXberg() cwd = %q, want %q", pwd, want)
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
