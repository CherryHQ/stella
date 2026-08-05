package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
)

type repeatingReader struct{ remaining int64 }

func (r *repeatingReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	for i := range buffer {
		buffer[i] = 'x'
	}
	r.remaining -= int64(len(buffer))
	return len(buffer), nil
}

func TestWorkspaceListRejectsNonPositiveDepthBeforeCallback(t *testing.T) {
	s, runtime := workspaceServer(t)
	depth := 0
	response := httptest.NewRecorder()
	s.GetSessionWorkspace(response, workspaceRequest(http.MethodGet, nil), "a1", "s1", apiserver.GetSessionWorkspaceParams{Depth: &depth})
	if response.Code != http.StatusBadRequest || runtime.calls != 0 {
		t.Fatalf("status=%d callbacks=%d, want 400 and no callback", response.Code, runtime.calls)
	}
}

func TestWorkspaceMoveMapsExistingDestinationToConflict(t *testing.T) {
	s, runtime := workspaceServer(t)
	filesystem := runtime.filesystem.(*workspaceTestFilesystem)
	filesystem.files["/workspace/source.txt"] = []byte("source")
	filesystem.files["/workspace/destination.txt"] = []byte("destination")
	scope := apitypes.WorkspaceScopeAgent
	response := httptest.NewRecorder()
	s.MoveWorkspaceFile(response, workspaceRequest(http.MethodPatch, strings.NewReader(`{"path":"source.txt","new_path":"destination.txt"}`)), "a1", "s1", apiserver.MoveWorkspaceFileParams{Scope: &scope})
	if response.Code != http.StatusConflict || runtime.calls != 1 || string(filesystem.files["/workspace/source.txt"]) != "source" || string(filesystem.files["/workspace/destination.txt"]) != "destination" {
		t.Fatalf("status=%d callbacks=%d files=%q/%q", response.Code, runtime.calls, filesystem.files["/workspace/source.txt"], filesystem.files["/workspace/destination.txt"])
	}
}

func TestWorkspaceCanonicalSandboxMarkersRemainReadable(t *testing.T) {
	s, runtime := workspaceServer(t)
	filesystem := runtime.filesystem.(*workspaceTestFilesystem)
	filesystem.files["/user/assets/legacy.txt"] = []byte("user")
	filesystem.files["/workspace/legacy.txt"] = []byte("agent")
	for _, test := range []struct{ path, want string }{{"/user/assets/legacy.txt", "user"}, {"/workspace/legacy.txt", "agent"}} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			s.GetWorkspaceFileContent(response, workspaceRequest(http.MethodGet, nil), "a1", "s1", apiserver.GetWorkspaceFileContentParams{Path: test.path})
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestWorkspaceUploadRemovesTemporaryMultipartFile(t *testing.T) {
	s, _ := workspaceServer(t)
	const spoolBytes int64 = 32<<20 + 1
	body := io.MultiReader(
		strings.NewReader("--cleanup\r\nContent-Disposition: form-data; name=\"file\"; filename=\"spooled.bin\"\r\n\r\n"),
		&repeatingReader{remaining: spoolBytes},
		strings.NewReader("\r\n--cleanup--\r\n"),
	)
	request := workspaceRequest(http.MethodPost, body)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=cleanup")
	response := httptest.NewRecorder()
	s.UploadWorkspaceFile(response, request, "a1", "s1")
	if response.Code != http.StatusCreated || request.MultipartForm == nil || len(request.MultipartForm.File["file"]) != 1 {
		t.Fatalf("status=%d form=%#v", response.Code, request.MultipartForm)
	}
	file, err := request.MultipartForm.File["file"][0].Open()
	if err == nil {
		_ = file.Close()
		t.Fatal("spooled multipart file remained after handler return")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reopen cleaned multipart file: %v, want not exist", err)
	}
}

func TestWorkspaceUploadHeaderSizeValidation(t *testing.T) {
	for _, size := range []int64{-1, workspaceUploadMaxFileBytes + 1} {
		if validWorkspaceUploadSize(size) {
			t.Fatalf("size %d accepted", size)
		}
	}
	if !validWorkspaceUploadSize(workspaceUploadMaxFileBytes) {
		t.Fatal("maximum upload size rejected")
	}
}

func TestWorkspaceUploadBodyLimitRejectsBeforeRuntimeCallback(t *testing.T) {
	s, runtime := workspaceServer(t)
	body := io.MultiReader(
		strings.NewReader("--limit\r\nContent-Disposition: form-data; name=\"file\"; filename=\"too-large.bin\"\r\n\r\n"),
		&repeatingReader{remaining: workspaceUploadMaxFileBytes + workspaceUploadMultipartOverhead + 1},
		strings.NewReader("\r\n--limit--\r\n"),
	)
	request := workspaceRequest(http.MethodPost, body)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=limit")
	response := httptest.NewRecorder()
	s.UploadWorkspaceFile(response, request, "a1", "s1")
	if response.Code != http.StatusRequestEntityTooLarge || runtime.calls != 0 {
		t.Fatalf("status=%d callbacks=%d, want 413 and no callback", response.Code, runtime.calls)
	}
}
