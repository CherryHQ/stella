package library

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/resources/binaries"
)

func TestLinuxLibraryScannedPDFE2E(t *testing.T) {
	// Exercise the managed binary that a Linux Stella deployment actually
	// starts; a stubbed command runner would not cover the Xberg bridge contract.
	toolHome := t.TempDir()
	if err := binaries.EnsureTools(toolHome); err != nil {
		t.Fatalf("extract managed tools: %v", err)
	}
	xberg := binaries.ToolPath(toolHome, "xberg")
	if xberg == "" {
		t.Fatal("managed Linux Xberg binary is missing")
	}

	fixture := filepath.Join(t.TempDir(), "scanned-policy.pdf")
	writeImageOnlyPDFFixture(t, fixture)

	const marker = "linuxocrmarker42"
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("Vision request = %s %s", request.Method, request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"linux-e2e","object":"chat.completion","created":1,"model":"fixture-vision","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"STELLA_OCR_V1:TEXT\n`+marker+` approved"}}]}`)
	}))
	defer provider.Close()

	parser, err := NewXbergCLIParser(t.Context(), xberg, func(context.Context) (VisionOCRConfig, error) {
		return VisionOCRConfig{
			ProviderID: "linux-e2e", ProviderType: "openai", Enabled: true,
			Model: "fixture-vision", BaseURL: provider.URL + "/v1", APIKey: "fixture-key",
		}, nil
	})
	if err != nil {
		t.Fatalf("create real Xberg parser: %v", err)
	}

	database := dbtest.New(t)
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	service, client := newWorkingLibraryService(t, database, store, parser)

	raw, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file, createErr := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", filepath.Base(fixture), raw,
	)
	_ = raw.Close()
	if createErr != nil {
		t.Fatalf("upload scanned PDF: %v", createErr)
	}

	// Real Xberg starts twice for an OCR document, so allow more time than the
	// in-memory parser integration tests while still bounding the background job.
	deadline := time.Now().Add(45 * time.Second)
	var ready LibraryFile
	for time.Now().Before(deadline) {
		ready, err = service.Get(t.Context(), file.ID)
		if err == nil && ready.Status == FileStatusReady && ready.ActiveChunkSetID != "" {
			break
		}
		if err == nil && ready.Status == FileStatusFailed {
			t.Fatalf("OCR derivation failed: %s", ready.ErrorMessage)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ready.Status != FileStatusReady || ready.ActiveChunkSetID == "" {
		t.Fatalf("scanned PDF did not become ready: %+v (error %v)", ready, err)
	}
	assertLatestLibraryJobState(t, client, chunkArgs{}.Kind(), file.ID, "completed")
	if providerCalls.Load() == 0 {
		t.Fatal("scanned PDF reached ready without an OCR provider call")
	}

	authority, err := authz.NewAgentAuthority(authz.UserID(testUserA), authz.AgentID(testAgentA))
	if err != nil {
		t.Fatal(err)
	}
	hits, err := service.Search(t.Context(), authority, marker, MaxSearchLimit)
	if err != nil {
		t.Fatalf("search OCR text: %v", err)
	}
	if len(hits) != 1 || hits[0].FileName != filepath.Base(fixture) ||
		!strings.Contains(hits[0].Content, marker) || hits[0].Locator == nil ||
		hits[0].Locator.FirstPage == nil || *hits[0].Locator.FirstPage != 1 ||
		hits[0].Locator.LastPage == nil || *hits[0].Locator.LastPage != 1 {
		t.Fatalf("OCR search hit = %+v, want marker with page 1 locator", hits)
	}
}

// writeImageOnlyPDFFixture creates a valid one-page PDF whose page consists
// solely of a full-page raster image. It keeps the integration fixture small,
// deterministic, and source-controlled as code instead of committing a binary.
func writeImageOnlyPDFFixture(t *testing.T, path string) {
	t.Helper()
	const width, height = 256, 256
	pixels := bytes.Repeat([]byte{0xff}, width*height*3)
	// Draw several dark bars so the raster is visibly non-blank while retaining
	// no native PDF text layer for Xberg to extract.
	for y := 24; y < height-24; y += 24 {
		for x := 20; x < width-20; x++ {
			if (x/36)%2 == 1 && x%36 < 8 {
				continue
			}
			for dy := range 5 {
				offset := ((y+dy)*width + x) * 3
				pixels[offset], pixels[offset+1], pixels[offset+2] = 0, 0, 0
			}
		}
	}

	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(pixels); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	content := []byte("q\n612 0 0 792 0 0 cm\n/Im0 Do\nQ\n")
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /ProcSet [/PDF /ImageC] /XObject << /Im0 4 0 R >> >> /Contents 5 0 R >>"),
		append(fmt.Appendf(nil, "<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", width, height, compressed.Len()), append(compressed.Bytes(), []byte("\nendstream")...)...),
		append(fmt.Appendf(nil, "<< /Length %d >>\nstream\n", len(content)), append(content, []byte("endstream")...)...),
	}

	var document bytes.Buffer
	document.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = document.Len()
		_, _ = fmt.Fprintf(&document, "%d 0 obj\n", index+1)
		document.Write(object)
		document.WriteString("\nendobj\n")
	}
	xref := document.Len()
	_, _ = fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		_, _ = fmt.Fprintf(&document, "%010d 00000 n \n", offsets[index])
	}
	_, _ = fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	if err := os.WriteFile(path, document.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
