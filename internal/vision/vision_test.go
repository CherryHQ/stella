package vision

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// xbergTestTimeout bounds test-only process startup and completion under the
// combined race and coverage gate without changing production cleanup limits.
const xbergTestTimeout = 5 * time.Second

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

func installXbergScript(t *testing.T, script string) string {
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
	if err := os.WriteFile(shim, []byte("#!/bin/sh\n[ \"$1\" = extract ] || exit 1\n"+script), 0o755); err != nil {
		t.Fatalf("write Xberg shim: %v", err)
	}
	return stellaHome
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
			want := installXbergShim(t, "xberg visible text")
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
	want := installXbergShim(t, "OCR only")
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
			data := encodedImage(t, mime, MaxImageDim+1, 2)
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

func TestBaselineStagesOwnedBytesForManagedXberg(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script and POSIX modes")
	}
	capture := t.TempDir()
	t.Setenv("CAPTURE_DIR", capture)
	installXbergScript(t, "pwd > \"$CAPTURE_DIR/cwd\"\nprintf %s \"$2\" > \"$CAPTURE_DIR/path\"\ncat \"$2\" > \"$CAPTURE_DIR/data\"\nls -ld \"$PWD\" > \"$CAPTURE_DIR/dirmode\"\nls -l \"$2\" > \"$CAPTURE_DIR/mode\"\nprintf 'OCR'\n")
	want := pngBytes(t, 8, 8)
	if _, err := New(Options{}).Baseline(context.Background(), Request{Data: want, MimeType: "image/png"}); err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	cwd, _ := os.ReadFile(filepath.Join(capture, "cwd"))
	path, _ := os.ReadFile(filepath.Join(capture, "path"))
	gotPath := strings.TrimSpace(string(path))
	if normalizeTestPath(strings.TrimSpace(string(cwd))) != normalizeTestPath(filepath.Dir(gotPath)) {
		t.Fatalf("Xberg cwd = %q, file dir = %q", cwd, filepath.Dir(gotPath))
	}
	if filepath.Base(gotPath) != "image.png" {
		t.Errorf("staged file = %q, want fixed image.png", gotPath)
	}
	if got, _ := os.ReadFile(filepath.Join(capture, "data")); !bytes.Equal(got, want) {
		t.Error("Xberg did not receive the exact validated bytes")
	}
	if mode, _ := os.ReadFile(filepath.Join(capture, "mode")); !strings.HasPrefix(string(mode), "-rw-------") {
		t.Errorf("staged file mode = %q, want 0600", mode)
	}
	if mode, _ := os.ReadFile(filepath.Join(capture, "dirmode")); !strings.HasPrefix(string(mode), "drwx------") {
		t.Errorf("staging directory mode = %q, want 0700", mode)
	}
	if _, err := os.Stat(filepath.Dir(gotPath)); !os.IsNotExist(err) {
		t.Errorf("staging directory survives Baseline: %v", err)
	}
}

func normalizeTestPath(path string) string {
	// macOS exposes /var through a /private/var symlink; the shell's pwd
	// canonicalizes it even though exec.Cmd received the same lexical directory.
	return strings.TrimPrefix(filepath.Clean(path), "/private")
}

func TestBaselineOwnsBytesAcrossModelFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	capture := t.TempDir()
	t.Setenv("CAPTURE_DIR", capture)
	installXbergScript(t, "cat \"$2\" > \"$CAPTURE_DIR/data\"\nprintf OCR\n")
	started := make(chan ai.Context, 1)
	release := make(chan struct{})
	build := func(_, _, _ string) (providers.StreamFunc, error) {
		stream := func(_ context.Context, _ ai.Model, ctx ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
			started <- ctx
			<-release
			s := providers.NewChannelEventStream(2)
			go func() {
				s.Emit(ai.EventTextDelta{Text: "invalid"})
				s.Emit(ai.EventStop{Reason: ai.StopReasonStop})
				s.Finish(nil)
			}()
			return s, nil
		}
		return stream, nil
	}
	data := pngBytes(t, 8, 8)
	want := append([]byte(nil), data...)
	done := make(chan error, 1)
	go func() {
		_, err := New(testOptions(build)).Baseline(context.Background(), Request{Data: data, MimeType: "image/png"})
		done <- err
	}()
	modelCtx := <-started
	for i := range data {
		data[i] ^= 0xff
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	userMessage := modelCtx.Messages[0].(ai.UserMessage)
	imageBlock := userMessage.Content.([]ai.ContentBlock)[1].(ai.ImageContent)
	modelBytes, err := base64.StdEncoding.DecodeString(imageBlock.Data)
	if err != nil || !bytes.Equal(modelBytes, want) {
		t.Fatalf("VLM received %d bytes / %v, want owned snapshot", len(modelBytes), err)
	}
	if got, _ := os.ReadFile(filepath.Join(capture, "data")); !bytes.Equal(got, want) {
		t.Error("Xberg fallback did not receive the same owned snapshot")
	}
}

func TestBaselineRejectsBeforeCommandForOversizeAndMIME(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	capture := t.TempDir()
	t.Setenv("CAPTURE_DIR", capture)
	installXbergScript(t, "touch \"$CAPTURE_DIR/ran\"\nprintf OCR\n")
	if _, err := New(Options{}).Baseline(context.Background(), Request{Data: make([]byte, MaxImageInputBytes+1), MimeType: "image/png"}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized Baseline error = %v", err)
	}
	if _, err := New(Options{}).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/jpeg"}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("MIME mismatch error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(capture, "ran")); !os.IsNotExist(err) {
		t.Errorf("Xberg ran for rejected input: %v", err)
	}
}

func TestXbergOutputAndProcessErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	t.Run("oversized output is reaped", func(t *testing.T) {
		capture := t.TempDir()
		t.Setenv("CAPTURE_DIR", capture)
		installXbergScript(t, "echo $$ > \"$CAPTURE_DIR/pid\"\nhead -c 262145 /dev/zero\nwhile :; do :; done\n")
		_, err := New(Options{}).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
		if err == nil || !strings.Contains(err.Error(), "output exceeds") {
			t.Fatalf("Baseline oversized output error = %v", err)
		}
		pidText, _ := os.ReadFile(filepath.Join(capture, "pid"))
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidText)))
		if parseErr != nil {
			t.Fatalf("read child pid: %v", parseErr)
		}
		deadline := time.Now().Add(xbergTestTimeout)
		gone := processGone(pid)
		for !gone && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
			gone = processGone(pid)
		}
		if !gone {
			t.Errorf("oversized-output child %d remains alive", pid)
		}
	})
	for name, script := range map[string]string{
		"command failure": "exit 7\n",
		"empty output":    "exit 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			installXbergScript(t, script)
			if _, err := New(Options{}).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"}); err == nil {
				t.Fatal("Baseline succeeded")
			}
		})
	}
	t.Run("cancellation", func(t *testing.T) {
		installXbergScript(t, "while :; do :; done\n")
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(10 * time.Millisecond); cancel() }()
		if _, err := New(Options{}).Baseline(ctx, Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"}); err == nil {
			t.Fatal("Baseline succeeded after cancellation")
		}
	})
}

func TestXbergTerminatesDescendantsHoldingStagingResources(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process-group assertions do not run on Windows")
	}
	for _, tt := range []struct {
		name       string
		scriptTail string
		cancel     bool
	}{
		{name: "cancellation", scriptTail: "wait\n", cancel: true},
		{name: "oversized output", scriptTail: "head -c 262145 /dev/zero\nwait\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			capture := t.TempDir()
			t.Setenv("CAPTURE_DIR", capture)
			installXbergScript(t, "echo $$ > \"$CAPTURE_DIR/root\"\n(while :; do :; done) &\necho $! > \"$CAPTURE_DIR/child\"\npwd > \"$CAPTURE_DIR/cwd\"\n"+tt.scriptTail)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := New(Options{}).Baseline(ctx, Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
				done <- err
			}()
			root := waitForTestPID(t, filepath.Join(capture, "root"))
			child := waitForTestPID(t, filepath.Join(capture, "child"))
			t.Cleanup(func() {
				killProcessGroup(root)
				killProcessGroup(child)
			})
			if tt.cancel {
				cancel()
			}
			select {
			case err := <-done:
				if tt.cancel {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("Baseline cancellation error = %v, want context.Canceled", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "output exceeds") {
					t.Fatalf("Baseline oversized output error = %v", err)
				}
			case <-time.After(xbergTestTimeout):
				killProcessGroup(root)
				t.Fatalf("Baseline did not terminate Xberg process tree within %s", xbergTestTimeout)
			}
			if !processGone(root) || !processGone(child) {
				t.Fatalf("Xberg process tree remains: root gone=%t child gone=%t", processGone(root), processGone(child))
			}
			cwd, err := os.ReadFile(filepath.Join(capture, "cwd"))
			if err != nil {
				t.Fatalf("read staging cwd: %v", err)
			}
			if _, err := os.Stat(strings.TrimSpace(string(cwd))); !os.IsNotExist(err) {
				t.Errorf("staging directory survives process-tree termination: %v", err)
			}
		})
	}
}

func TestXbergReapsLingeringDescendantAfterSuccessfulRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process-group assertions do not run on Windows")
	}
	capture := t.TempDir()
	t.Setenv("CAPTURE_DIR", capture)
	installXbergScript(t, "(while :; do :; done) &\necho $! > \"$CAPTURE_DIR/child\"\nprintf OCR\nexit 0\n")
	done := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		baseline, err := New(Options{}).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
		done <- struct {
			text string
			err  error
		}{baseline.Text, err}
	}()
	child := waitForTestPID(t, filepath.Join(capture, "child"))
	t.Cleanup(func() { killProcessGroup(child) })
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Baseline: %v", result.err)
		}
		if !strings.Contains(result.text, "OCR") {
			t.Fatalf("Baseline text = %q, want normalized OCR", result.text)
		}
	case <-time.After(xbergTestTimeout):
		t.Fatalf("Baseline waited for a descendant holding stdout after Xberg exited within %s", xbergTestTimeout)
	}
	if !processGone(child) {
		t.Fatalf("successful Xberg root left descendant %d alive", child)
	}
}

func TestXbergFailsClosedForEscapedDescendantHoldingStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process-group assertions do not run on Windows")
	}
	oldDrainWait := xbergDrainWait
	xbergDrainWait = 50 * time.Millisecond
	t.Cleanup(func() { xbergDrainWait = oldDrainWait })
	capture := t.TempDir()
	t.Setenv("CAPTURE_DIR", capture)
	t.Setenv("XBERG_TEST_BINARY", os.Args[0])
	// The helper writes escaped-ready only after syscall.Setsid succeeds. The
	// shim waits for that evidence before it exits, so group cancellation cannot
	// race ahead and kill the child before it escapes the supervisor's group.
	installXbergScript(t, "STELLA_XBERG_ESCAPED_DESCENDANT_HELPER=1 STELLA_XBERG_ESCAPED_DESCENDANT_READY=\"$CAPTURE_DIR/escaped-ready\" \"$XBERG_TEST_BINARY\" -test.run '^TestXbergEscapedDescendantHelper$' &\nchild=$!\nwhile [ ! -s \"$CAPTURE_DIR/escaped-ready\" ]; do\n\tif ! kill -0 \"$child\" 2>/dev/null; then exit 1; fi\n\tsleep 0.01\ndone\ncat \"$CAPTURE_DIR/escaped-ready\" > \"$CAPTURE_DIR/child\"\nprintf OCR\nexit 0\n")
	done := make(chan error, 1)
	go func() {
		_, err := New(Options{}).Baseline(context.Background(), Request{Data: pngBytes(t, 8, 8), MimeType: "image/png"})
		done <- err
	}()
	child := waitForTestPID(t, filepath.Join(capture, "child"))
	t.Cleanup(func() { killProcessGroup(child) })
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not drain") {
			t.Fatalf("Baseline escaped-descendant error = %v, want bounded drain failure", err)
		}
	case <-time.After(xbergTestTimeout):
		t.Fatalf("Baseline hung on an escaped descendant holding stdout within %s", xbergTestTimeout)
	}
}

func waitForTestPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(xbergTestTimeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for child pid at %s", path)
	return 0
}

func TestXbergArchitectureHasNoPublicCallerPathAPI(t *testing.T) {
	source, err := os.ReadFile("xberg.go")
	if err != nil {
		t.Fatalf("read production xberg source: %v", err)
	}
	text := string(source)
	if strings.Contains(text, "func ExtractWithXberg(") {
		t.Fatal("production vision must not expose a caller-path Xberg API")
	}
	if strings.Count(text, "runXberg(") != 2 || !strings.Contains(text, "return runXberg(ctx, dir, path)") {
		t.Fatal("the sole Xberg invocation must receive only daemon-staged path and cwd")
	}
	guard := strings.Index(text, "if err := xbergFallbackSupported(); err != nil")
	staging := strings.Index(text, "os.MkdirTemp")
	if guard < 0 || staging < 0 || guard > staging {
		t.Fatal("Xberg platform guard must fail before staging files")
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
