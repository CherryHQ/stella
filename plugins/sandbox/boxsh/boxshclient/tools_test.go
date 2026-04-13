package boxshclient

import (
	"encoding/json"
	"testing"
)

func TestExecParamsMarshal(t *testing.T) {
	params := ExecParams{
		Command: "echo hello",
		Timeout: 30,
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ExecParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Command != params.Command {
		t.Errorf("Command = %q, want %q", decoded.Command, params.Command)
	}
	if decoded.Timeout != params.Timeout {
		t.Errorf("Timeout = %d, want %d", decoded.Timeout, params.Timeout)
	}
}

func TestExecResultUnmarshal(t *testing.T) {
	jsonData := `{"stdout":"hello","stderr":"","exit_code":0}`
	var result ExecResult
	if err := json.Unmarshal([]byte(jsonData), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.Stdout != "hello" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "hello")
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestReadParamsMarshal(t *testing.T) {
	params := ReadParams{
		FilePath: "/path/to/file.txt",
		Offset:   10,
		Limit:    100,
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ReadParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.FilePath != params.FilePath {
		t.Errorf("FilePath = %q, want %q", decoded.FilePath, params.FilePath)
	}
	if decoded.Offset != params.Offset {
		t.Errorf("Offset = %d, want %d", decoded.Offset, params.Offset)
	}
	if decoded.Limit != params.Limit {
		t.Errorf("Limit = %d, want %d", decoded.Limit, params.Limit)
	}
}

func TestReadResultUnmarshal(t *testing.T) {
	jsonData := `{"content":"file contents","total_lines":50,"truncated":true}`
	var result ReadResult
	if err := json.Unmarshal([]byte(jsonData), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.Content != "file contents" {
		t.Errorf("Content = %q, want %q", result.Content, "file contents")
	}
	if result.TotalLines != 50 {
		t.Errorf("TotalLines = %d, want 50", result.TotalLines)
	}
	if !result.Truncated {
		t.Error("Truncated should be true")
	}
}

func TestWriteParamsMarshal(t *testing.T) {
	params := WriteParams{
		FilePath: "/path/to/file.txt",
		Content:  "hello world",
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded WriteParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.FilePath != params.FilePath {
		t.Errorf("FilePath = %q, want %q", decoded.FilePath, params.FilePath)
	}
	if decoded.Content != params.Content {
		t.Errorf("Content = %q, want %q", decoded.Content, params.Content)
	}
}

func TestWriteResultUnmarshal(t *testing.T) {
	jsonData := `{"bytes_written":100,"path":"/path/to/file.txt"}`
	var result WriteResult
	if err := json.Unmarshal([]byte(jsonData), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.BytesWritten != 100 {
		t.Errorf("BytesWritten = %d, want 100", result.BytesWritten)
	}
	if result.Path != "/path/to/file.txt" {
		t.Errorf("Path = %q, want %q", result.Path, "/path/to/file.txt")
	}
}

func TestEditParamsMarshal(t *testing.T) {
	params := EditParams{
		FilePath: "/path/to/file.txt",
		Edits:    []EditSpec{{OldText: "old", NewText: "new"}},
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded EditParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.FilePath != params.FilePath {
		t.Errorf("FilePath = %q, want %q", decoded.FilePath, params.FilePath)
	}
	if len(decoded.Edits) != 1 || decoded.Edits[0].OldText != "old" || decoded.Edits[0].NewText != "new" {
		t.Errorf("Edits = %+v, want one old/new replacement", decoded.Edits)
	}
}

func TestEditResultUnmarshal(t *testing.T) {
	jsonData := `{"path":"/path/to/file.txt","replacements":1}`
	var result EditResult
	if err := json.Unmarshal([]byte(jsonData), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if result.Path != "/path/to/file.txt" {
		t.Errorf("Path = %q, want %q", result.Path, "/path/to/file.txt")
	}
	if result.Replacements != 1 {
		t.Errorf("Replacements = %d, want 1", result.Replacements)
	}
}

func TestListParamsMarshal(t *testing.T) {
	params := ListParams{DirPath: "/path/to/dir"}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ListParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.DirPath != params.DirPath {
		t.Errorf("DirPath = %q, want %q", decoded.DirPath, params.DirPath)
	}
}

func TestListResultUnmarshal(t *testing.T) {
	jsonData := `{"entries":[{"name":"file.txt","is_dir":false,"size":100},{"name":"subdir","is_dir":true,"size":0}]}`
	var result ListResult
	if err := json.Unmarshal([]byte(jsonData), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(result.Entries) != 2 {
		t.Errorf("len(Entries) = %d, want 2", len(result.Entries))
	}
	if result.Entries[0].Name != "file.txt" {
		t.Errorf("Entries[0].Name = %q, want %q", result.Entries[0].Name, "file.txt")
	}
	if result.Entries[0].IsDir {
		t.Error("Entries[0].IsDir should be false")
	}
	if result.Entries[0].Size != 100 {
		t.Errorf("Entries[0].Size = %d, want 100", result.Entries[0].Size)
	}
	if !result.Entries[1].IsDir {
		t.Error("Entries[1].IsDir should be true")
	}
}

func TestStatParamsMarshal(t *testing.T) {
	params := StatParams{Path: "/path/to/file.txt"}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded StatParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Path != params.Path {
		t.Errorf("Path = %q, want %q", decoded.Path, params.Path)
	}
}

func TestStatResultUnmarshal(t *testing.T) {
	jsonData := `{"exists":true,"is_dir":false,"size":1024,"mod_time":"2024-01-15T10:30:00Z"}`
	var result StatResult
	if err := json.Unmarshal([]byte(jsonData), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !result.Exists {
		t.Error("Exists should be true")
	}
	if result.IsDir {
		t.Error("IsDir should be false")
	}
	if result.Size != 1024 {
		t.Errorf("Size = %d, want 1024", result.Size)
	}
	if result.ModTime != "2024-01-15T10:30:00Z" {
		t.Errorf("ModTime = %q, want %q", result.ModTime, "2024-01-15T10:30:00Z")
	}
}
