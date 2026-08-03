package knowledge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/blob"
)

func TestFSRawStoreContract(t *testing.T) {
	t.Parallel()
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	runRawStoreContract(t, store)
}

func TestS3RawStoreContract(t *testing.T) {
	t.Parallel()
	server := newRawS3Server()
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	endpoint := strings.TrimPrefix(httpServer.URL, "http://")
	store, err := NewS3RawStore(blob.S3Config{
		Endpoint: endpoint, Bucket: "knowledge-test", AccessKey: "test", SecretKey: "test", UseSSL: false,
	}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runRawStoreContract(t, store)
	if err := server.putContractError(); err != nil {
		t.Fatal(err)
	}
}

func runRawStoreContract(t *testing.T, store RawStore) {
	t.Helper()
	ctx := t.Context()
	want := make(map[string][]byte)
	for index := range 5 {
		key := mustRawKey(t)
		content := []byte("document-" + strconv.Itoa(index))
		if err := store.Create(ctx, key, bytes.NewReader(content)); err != nil {
			t.Fatalf("Create(%q): %v", key, err)
		}
		want[key] = content
	}

	collisionKey := mustRawKey(t)
	contents := [][]byte{[]byte("first contender"), []byte("second contender")}
	start := make(chan struct{})
	errorsByIndex := make([]error, len(contents))
	var wait sync.WaitGroup
	for index := range contents {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			errorsByIndex[index] = store.Create(ctx, collisionKey, bytes.NewReader(contents[index]))
		}(index)
	}
	close(start)
	wait.Wait()
	var winner []byte
	var successes, conflicts int
	for index, err := range errorsByIndex {
		switch {
		case err == nil:
			successes++
			winner = contents[index]
		case errors.Is(err, ErrRawAlreadyExists):
			conflicts++
		default:
			t.Fatalf("concurrent Create error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent Create = %d successes, %d conflicts", successes, conflicts)
	}
	want[collisionKey] = winner

	opened, err := store.Open(ctx, collisionKey)
	if err != nil {
		t.Fatal(err)
	}
	gotContent, readErr := io.ReadAll(opened)
	closeErr := opened.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read collision winner: read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(gotContent, winner) {
		t.Fatalf("collision winner content = %q, want %q", gotContent, winner)
	}

	seen := make(map[string]RawObject)
	var cursor string
	for pages := 0; ; pages++ {
		if pages > len(want) {
			t.Fatal("ListPage cursor did not converge")
		}
		page, err := store.ListPage(ctx, RawPrefix, cursor, 2)
		if err != nil {
			t.Fatalf("ListPage(%q): %v", cursor, err)
		}
		if len(page.Objects) > 2 {
			t.Fatalf("page contains %d objects, limit 2", len(page.Objects))
		}
		for _, object := range page.Objects {
			if _, duplicate := seen[object.Key]; duplicate {
				t.Fatalf("duplicate paginated key %q", object.Key)
			}
			if object.Size != int64(len(want[object.Key])) {
				t.Fatalf("size for %q = %d, want %d", object.Key, object.Size, len(want[object.Key]))
			}
			if object.LastModified.IsZero() {
				t.Fatalf("LastModified for %q is zero", object.Key)
			}
			seen[object.Key] = object
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatalf("cursor did not advance from %q", cursor)
		}
		cursor = page.NextCursor
	}
	if len(seen) != len(want) {
		t.Fatalf("listed %d objects, want %d", len(seen), len(want))
	}

	if err := store.Delete(ctx, collisionKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, collisionKey); err != nil {
		t.Fatalf("idempotent Delete: %v", err)
	}
}

func TestFSRawStoreRejectsBelowLowWater(t *testing.T) {
	t.Parallel()
	store, err := NewFSRawStore(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	store.availableBytes = func(string) (int64, error) { return 99, nil }
	key := mustRawKey(t)
	if err := store.Create(t.Context(), key, strings.NewReader("content")); !errors.Is(err, ErrRawStorageDegraded) {
		t.Fatalf("Create error = %v, want ErrRawStorageDegraded", err)
	}
	if _, err := store.Open(t.Context(), key); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("degraded Create published an object: %v", err)
	}
}

func TestS3RawStoreAdmissionRejectsBeforePublish(t *testing.T) {
	t.Parallel()
	server := newRawS3Server()
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	store, err := NewS3RawStore(blob.S3Config{
		Endpoint: strings.TrimPrefix(httpServer.URL, "http://"),
		Bucket:   "knowledge-test", AccessKey: "test", SecretKey: "test",
	}, t.TempDir(), func(context.Context) error { return errors.New("backlog threshold exceeded") })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(t.Context(), mustRawKey(t), strings.NewReader("content")); !errors.Is(err, ErrRawStorageDegraded) {
		t.Fatalf("Create error = %v, want ErrRawStorageDegraded", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.puts != 0 {
		t.Fatalf("S3 received %d PUTs after admission rejection", server.puts)
	}
}

func TestRawStoreListPageRejectsUnboundedRequests(t *testing.T) {
	t.Parallel()
	store, err := NewFSRawStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, MaxRawListPageSize + 1} {
		if _, err := store.ListPage(t.Context(), RawPrefix, "", limit); !errors.Is(err, ErrInvalidRawStorePage) {
			t.Fatalf("limit %d error = %v", limit, err)
		}
	}
}

func mustRawKey(t *testing.T) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	key, err := RawKey(id)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type rawS3Object struct {
	content      []byte
	lastModified time.Time
}

type rawS3Server struct {
	mu                 sync.Mutex
	objects            map[string]rawS3Object
	puts               int
	missingConditional bool
	multipartPut       bool
}

func newRawS3Server() *rawS3Server {
	return &rawS3Server{objects: make(map[string]rawS3Object)}
}

func (s *rawS3Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if (request.URL.Path == "/knowledge-test" || request.URL.Path == "/knowledge-test/") &&
		request.URL.Query().Has("location") {
		writer.Header().Set("Content-Type", "application/xml")
		_, _ = writer.Write([]byte(
			`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`,
		))
		return
	}
	if request.URL.Query().Get("list-type") == "2" {
		s.serveList(writer, request)
		return
	}
	key := strings.TrimPrefix(request.URL.Path, "/knowledge-test/")
	if key == request.URL.Path || key == "" {
		writeS3Error(writer, http.StatusNotFound, "NoSuchKey")
		return
	}
	switch request.Method {
	case http.MethodPut:
		content, err := readS3RequestBody(request)
		if err != nil {
			writeS3Error(writer, http.StatusInternalServerError, "ReadError")
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.puts++
		if request.Header.Get("If-None-Match") != "*" {
			s.missingConditional = true
		}
		if request.URL.Query().Has("uploads") || request.URL.Query().Has("uploadId") {
			s.multipartPut = true
		}
		if _, exists := s.objects[key]; exists {
			writeS3Error(writer, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		s.objects[key] = rawS3Object{content: append([]byte(nil), content...), lastModified: time.Now().UTC()}
		writer.Header().Set("ETag", `"test-etag"`)
		writer.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		s.mu.Lock()
		object, exists := s.objects[key]
		s.mu.Unlock()
		if !exists {
			writeS3Error(writer, http.StatusNotFound, "NoSuchKey")
			return
		}
		writer.Header().Set("Content-Length", strconv.Itoa(len(object.content)))
		writer.Header().Set("Last-Modified", object.lastModified.Format(http.TimeFormat))
		writer.Header().Set("ETag", `"test-etag"`)
		if request.Method == http.MethodGet {
			_, _ = writer.Write(object.content)
		}
	case http.MethodDelete:
		s.mu.Lock()
		delete(s.objects, key)
		s.mu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func readS3RequestBody(request *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Contains(raw, []byte(";chunk-signature=")) {
		return raw, err
	}
	reader := bufio.NewReader(bytes.NewReader(raw))
	var content bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeField := strings.SplitN(strings.TrimSpace(line), ";", 2)[0]
		size, err := strconv.ParseInt(sizeField, 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			_, err = reader.ReadString('\n')
			return content.Bytes(), err
		}
		if _, err := io.CopyN(&content, reader, size); err != nil {
			return nil, err
		}
		if _, err := reader.ReadString('\n'); err != nil {
			return nil, err
		}
	}
}

type rawS3ListResult struct {
	XMLName     xml.Name          `xml:"ListBucketResult"`
	Xmlns       string            `xml:"xmlns,attr,omitempty"`
	Name        string            `xml:"Name"`
	Prefix      string            `xml:"Prefix"`
	KeyCount    int               `xml:"KeyCount"`
	MaxKeys     int               `xml:"MaxKeys"`
	IsTruncated bool              `xml:"IsTruncated"`
	Contents    []rawS3ListObject `xml:"Contents"`
}

type rawS3ListObject struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

func (s *rawS3Server) serveList(writer http.ResponseWriter, request *http.Request) {
	prefix := request.URL.Query().Get("prefix")
	startAfter := request.URL.Query().Get("start-after")
	maxKeys, _ := strconv.Atoi(request.URL.Query().Get("max-keys"))
	if maxKeys < 1 {
		maxKeys = 1000
	}
	s.mu.Lock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) && key > startAfter {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxKeys {
		keys = keys[:maxKeys]
	}
	contents := make([]rawS3ListObject, 0, len(keys))
	for _, key := range keys {
		object := s.objects[key]
		contents = append(contents, rawS3ListObject{
			Key: key, LastModified: object.lastModified.Format(time.RFC3339), ETag: `"test-etag"`,
			Size: int64(len(object.content)), StorageClass: "STANDARD",
		})
	}
	s.mu.Unlock()
	result := rawS3ListResult{
		Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/", Name: "knowledge-test",
		Prefix: prefix, KeyCount: len(contents), MaxKeys: maxKeys, Contents: contents,
	}
	writer.Header().Set("Content-Type", "application/xml")
	_, _ = writer.Write([]byte(xml.Header))
	_ = xml.NewEncoder(writer).Encode(result)
}

func (s *rawS3Server) putContractError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.missingConditional {
		return errors.New("S3 RawStore omitted If-None-Match: *")
	}
	if s.multipartPut {
		return errors.New("S3 RawStore used multipart publication")
	}
	return nil
}

func writeS3Error(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/xml")
	writer.WriteHeader(status)
	_ = xml.NewEncoder(writer).Encode(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: code, Message: code})
}
