package knowledge

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestOwnerValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		owner Owner
		ok    bool
	}{
		{name: "system", owner: Owner{Scope: ScopeSystem}, ok: true},
		{name: "system agent", owner: Owner{Scope: ScopeSystemAgent, AgentID: "finance"}, ok: true},
		{name: "user", owner: Owner{Scope: ScopeUser, UserID: "user-1"}, ok: true},
		{name: "user agent", owner: Owner{Scope: ScopeUserAgent, UserID: "user-1", AgentID: "finance"}, ok: true},
		{name: "system with user", owner: Owner{Scope: ScopeSystem, UserID: "user-1"}},
		{name: "system agent without agent", owner: Owner{Scope: ScopeSystemAgent}},
		{name: "user with agent", owner: Owner{Scope: ScopeUser, UserID: "user-1", AgentID: "finance"}},
		{name: "user agent without user", owner: Owner{Scope: ScopeUserAgent, AgentID: "finance"}},
		{name: "unknown", owner: Owner{Scope: "other"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.owner.Validate()
			if (err == nil) != tt.ok {
				t.Fatalf("Validate() error = %v, ok = %t", err, tt.ok)
			}
		})
	}
}

func TestNormalizeSearchQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "collapses whitespace", input: "  差旅\n\t 报销  ", want: "差旅 报销"},
		{name: "drops invalid runes", input: "Alpha\x00\uFFFD-2026", want: "Alpha-2026"},
		{name: "punctuation only", input: " -- / … ", want: ""},
		{name: "keeps searchable identifiers", input: "C++ Alpha-2026 v1.2", want: "C++ Alpha-2026 v1.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeSearchQuery(tt.input); got != tt.want {
				t.Fatalf("normalizeSearchQuery(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateUpload(t *testing.T) {
	t.Parallel()

	docx := testDOCXBytes(t)
	tests := []struct {
		name      string
		fileName  string
		content   []byte
		wantName  string
		wantMedia string
		wantErr   error
	}{
		{
			name:      "pdf",
			fileName:  `C:\uploads\policy.PDF`,
			content:   []byte("%PDF-1.4\n"),
			wantName:  "policy.PDF",
			wantMedia: MediaTypePDF,
		},
		{
			name:      "docx",
			fileName:  "policy.docx",
			content:   docx,
			wantName:  "policy.docx",
			wantMedia: MediaTypeDOCX,
		},
		{
			name:      "markdown",
			fileName:  "policy.markdown",
			content:   []byte("# Policy\n"),
			wantName:  "policy.markdown",
			wantMedia: MediaTypeMarkdown,
		},
		{
			name:      "text",
			fileName:  "policy.txt",
			content:   []byte("Policy\n"),
			wantName:  "policy.txt",
			wantMedia: MediaTypeText,
		},
		{
			name:     "spoofed pdf",
			fileName: "policy.pdf",
			content:  []byte("plain text"),
			wantErr:  ErrInvalidFile,
		},
		{
			name:     "spoofed docx",
			fileName: "policy.docx",
			content:  []byte("PK"),
			wantErr:  ErrInvalidFile,
		},
		{
			name:     "binary text",
			fileName: "policy.txt",
			content:  []byte{'a', 0, 'b'},
			wantErr:  ErrInvalidFile,
		},
		{
			name:     "unsupported",
			fileName: "policy.xlsx",
			content:  []byte("x"),
			wantErr:  ErrUnsupportedFileType,
		},
		{
			name:     "too large",
			fileName: "policy.txt",
			content:  []byte(strings.Repeat("x", MaxFileBytes+1)),
			wantErr:  ErrFileTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateUpload(tt.fileName, tt.content)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateUpload() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if got.FileName != tt.wantName || got.MediaType != tt.wantMedia {
				t.Fatalf("ValidateUpload() = %#v", got)
			}
		})
	}
}

func testDOCXBytes(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range map[string]string{
		"[Content_Types].xml": "<Types/>",
		"word/document.xml":   "<document/>",
	} {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
