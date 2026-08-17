package library

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

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

func TestLibraryMixedPDFPreservesNativeOCRAndNoTextPages(t *testing.T) {
	const (
		nativeMarker = "mixednativemarker42"
		ocrMarker    = "mixedocrmarker42"
	)
	fixture := filepath.Join(t.TempDir(), "mixed-policy.pdf")
	writeMixedPDFFixture(t, fixture, nativeMarker)

	var providerCalls, textCalls, noTextCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		page, err := decodeOCRRequestImage(request)
		if err != nil {
			t.Errorf("decode Vision page: %v", err)
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		content := ocrProtocolNoText
		if imageHasDarkContent(page) {
			textCalls.Add(1)
			content = ocrProtocolText + ocrMarker
		} else {
			noTextCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"mixed-e2e","object":"chat.completion","created":1,"model":"fixture-vision","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":%q}}]}`, content)
	}))
	defer provider.Close()

	service, client := newRealXbergOCRService(t, provider.URL)
	raw, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file, createErr := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", filepath.Base(fixture), raw,
	)
	_ = raw.Close()
	if createErr != nil {
		t.Fatalf("upload mixed PDF: %v", createErr)
	}
	ready := waitForOCRE2EStatus(t, service, file.ID, FileStatusReady)
	assertLatestLibraryJobState(t, client, chunkArgs{}.Kind(), file.ID, "completed")
	if providerCalls.Load() != 2 || textCalls.Load() != 1 || noTextCalls.Load() != 1 {
		t.Fatalf("Vision calls total/text/no-text = %d/%d/%d, want 2/1/1", providerCalls.Load(), textCalls.Load(), noTextCalls.Load())
	}

	assertOCRSearchHit(t, service, nativeMarker, filepath.Base(fixture), 1)
	assertOCRSearchHit(t, service, ocrMarker, filepath.Base(fixture), 2)
	var content string
	if err := service.db.QueryRow(t.Context(), `
		SELECT coalesce(string_agg(content, '' ORDER BY ordinal), '')
		FROM library_chunk WHERE chunk_set_id = $1
	`, ready.ActiveChunkSetID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if strings.Count(content, nativeMarker) != 1 || strings.Count(content, ocrMarker) != 1 {
		t.Fatalf("mixed output duplicated or omitted markers: %q", content)
	}
	if strings.Index(content, nativeMarker) > strings.Index(content, ocrMarker) {
		t.Fatalf("mixed output lost source order: %q", content)
	}
}

func TestLibraryAllNoTextPDFFailsWithoutPublishing(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "no-text.pdf")
	writeImageOnlyPDFFixture(t, fixture)
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"no-text-e2e","object":"chat.completion","created":1,"model":"fixture-vision","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"STELLA_OCR_V1:NO_TEXT"}}]}`)
	}))
	defer provider.Close()

	service, client := newRealXbergOCRService(t, provider.URL)
	raw, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file, createErr := service.CreateManagedUpload(
		t.Context(), testAuthority(t, testUserA, true), ScopeSystem, "", filepath.Base(fixture), raw,
	)
	_ = raw.Close()
	if createErr != nil {
		t.Fatalf("upload no-text PDF: %v", createErr)
	}
	failed := waitForOCRE2EStatus(t, service, file.ID, FileStatusFailed)
	assertLatestLibraryJobState(t, client, chunkArgs{}.Kind(), file.ID, "completed")
	if providerCalls.Load() == 0 || failed.ActiveChunkSetID != "" || failed.ErrorMessage != "No extractable text was found in this document." {
		t.Fatalf("no-text result = calls %d file %+v", providerCalls.Load(), failed)
	}
	var chunks int
	if err := service.db.QueryRow(t.Context(), `
		SELECT count(*)
		FROM library_chunk AS chunk
		JOIN library_chunk_set AS chunk_set ON chunk_set.id = chunk.chunk_set_id
		WHERE chunk_set.file_id = $1
	`, file.ID).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 {
		t.Fatalf("no-text PDF staged %d chunks", chunks)
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

func newRealXbergOCRService(t *testing.T, providerURL string) (*Service, *river.Client[pgx.Tx]) {
	t.Helper()
	toolHome := t.TempDir()
	if err := binaries.EnsureTools(toolHome); err != nil {
		t.Fatalf("extract managed tools: %v", err)
	}
	xberg := binaries.ToolPath(toolHome, "xberg")
	if xberg == "" {
		t.Fatal("managed Xberg binary is missing")
	}
	parser, err := NewXbergCLIParser(t.Context(), xberg, func(context.Context) (VisionOCRConfig, error) {
		return VisionOCRConfig{
			ProviderID: "ocr-e2e", ProviderType: "openai", Enabled: true,
			Model: "fixture-vision", BaseURL: providerURL + "/v1", APIKey: "fixture-key",
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
	return service, client
}

func waitForOCRE2EStatus(t *testing.T, service *Service, fileID string, want FileStatus) LibraryFile {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		file, err := service.Get(t.Context(), fileID)
		if err == nil && file.Status == want {
			return file
		}
		if err == nil && (file.Status == FileStatusReady || file.Status == FileStatusFailed) {
			t.Fatalf("OCR derivation reached %q, want %q: %+v", file.Status, want, file)
		}
		time.Sleep(50 * time.Millisecond)
	}
	file, err := service.Get(t.Context(), fileID)
	t.Fatalf("OCR derivation did not reach %q: file=%+v error=%v", want, file, err)
	return LibraryFile{}
}

func assertOCRSearchHit(t *testing.T, service *Service, marker, fileName string, page uint32) {
	t.Helper()
	authority, err := authz.NewAgentAuthority(authz.UserID(testUserA), authz.AgentID(testAgentA))
	if err != nil {
		t.Fatal(err)
	}
	hits, err := service.Search(t.Context(), authority, marker, MaxSearchLimit)
	if err != nil {
		t.Fatalf("search %q: %v", marker, err)
	}
	if len(hits) != 1 || hits[0].FileName != fileName || !strings.Contains(hits[0].Content, marker) ||
		hits[0].Locator == nil || hits[0].Locator.FirstPage == nil || *hits[0].Locator.FirstPage > page ||
		hits[0].Locator.LastPage == nil || *hits[0].Locator.LastPage < page {
		t.Fatalf("search %q hit = %+v, want locator containing page %d", marker, hits, page)
	}
}

func decodeOCRRequestImage(request *http.Request) (image.Image, error) {
	var payload any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode OCR request: %w", err)
	}
	dataURL := findImageDataURL(payload)
	if dataURL == "" {
		return nil, fmt.Errorf("OCR request has no image data URL")
	}
	_, encoded, ok := strings.Cut(dataURL, ",")
	if !ok {
		return nil, fmt.Errorf("OCR image data URL is malformed")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode OCR image: %w", err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode OCR image pixels: %w", err)
	}
	return decoded, nil
}

func findImageDataURL(value any) string {
	switch typed := value.(type) {
	case string:
		if strings.HasPrefix(typed, "data:image/") {
			return typed
		}
	case []any:
		for _, item := range typed {
			if found := findImageDataURL(item); found != "" {
				return found
			}
		}
	case map[string]any:
		for _, item := range typed {
			if found := findImageDataURL(item); found != "" {
				return found
			}
		}
	}
	return ""
}

func imageHasDarkContent(source image.Image) bool {
	bounds := source.Bounds()
	total := bounds.Dx() * bounds.Dy()
	if total == 0 {
		return false
	}
	dark := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := source.At(x, y).RGBA()
			if (r+g+b)/(3*257) < 220 {
				dark++
			}
		}
	}
	return dark > total/1000
}

func writeMixedPDFFixture(t *testing.T, path, nativeMarker string) {
	t.Helper()
	const width, height = 256, 256
	patterned := bytes.Repeat([]byte{0xff}, width*height*3)
	for y := 40; y < height-40; y += 32 {
		for x := 24; x < width-24; x++ {
			for dy := range 6 {
				offset := ((y+dy)*width + x) * 3
				patterned[offset], patterned[offset+1], patterned[offset+2] = 0, 0, 0
			}
		}
	}
	blank := bytes.Repeat([]byte{0xff}, width*height*3)
	imageContent := []byte("q\n612 0 0 792 0 0 cm\n/Im0 Do\nQ\n")
	nativeContent := []byte("BT\n/F1 24 Tf\n72 720 Td\n(" + nativeMarker + ") Tj\nET\n")
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 6 0 R >> >> /Contents 7 0 R >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /ProcSet [/PDF /ImageC] /XObject << /Im0 8 0 R >> >> /Contents 9 0 R >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /ProcSet [/PDF /ImageC] /XObject << /Im0 10 0 R >> >> /Contents 11 0 R >>"),
		[]byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		pdfStreamObject(nativeContent),
		pdfImageObject(t, patterned, width, height),
		pdfStreamObject(imageContent),
		pdfImageObject(t, blank, width, height),
		pdfStreamObject(imageContent),
	}
	writePDFObjects(t, path, objects)
}

func pdfImageObject(t *testing.T, pixels []byte, width, height int) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(pixels); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	header := fmt.Appendf(nil, "<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", width, height, compressed.Len())
	return append(header, append(compressed.Bytes(), []byte("\nendstream")...)...)
}

func pdfStreamObject(content []byte) []byte {
	header := fmt.Appendf(nil, "<< /Length %d >>\nstream\n", len(content))
	return append(header, append(content, []byte("endstream")...)...)
}

func writePDFObjects(t *testing.T, path string, objects [][]byte) {
	t.Helper()
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
