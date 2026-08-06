package knowledge

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestOwnerValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		owner Owner
		valid bool
	}{
		{"system", Owner{Scope: ScopeSystem}, true},
		{"system agent", Owner{Scope: ScopeSystemAgent, AgentID: "a"}, true},
		{"user", Owner{Scope: ScopeUser, UserID: "u"}, true},
		{"user agent", Owner{Scope: ScopeUserAgent, UserID: "u", AgentID: "a"}, true},
		{"system with user", Owner{Scope: ScopeSystem, UserID: "u"}, false},
		{"system agent without agent", Owner{Scope: ScopeSystemAgent}, false},
		{"user without user", Owner{Scope: ScopeUser}, false},
		{"user agent without user", Owner{Scope: ScopeUserAgent, AgentID: "a"}, false},
		{"unknown", Owner{Scope: "unknown"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.owner.Validate()
			if (err == nil) != test.valid {
				t.Fatalf("Validate() error = %v, valid = %t", err, test.valid)
			}
		})
	}
}

func TestQuotaPoolIdentity(t *testing.T) {
	t.Parallel()
	user := Owner{Scope: ScopeUser, UserID: "u"}
	userAgent := Owner{Scope: ScopeUserAgent, UserID: "u", AgentID: "a"}
	if quotaLockKey(user) != quotaLockKey(userAgent) {
		t.Fatal("user and user_agent must share one personal quota lock")
	}
	if quotaLockKey(Owner{Scope: ScopeSystem}) == quotaLockKey(user) {
		t.Fatal("system and personal pools must not share a quota lock")
	}
	if quotaLockKey(Owner{Scope: ScopeSystemAgent, AgentID: "a"}) ==
		quotaLockKey(Owner{Scope: ScopeSystemAgent, AgentID: "b"}) {
		t.Fatal("different Agents must have independent system_agent quota locks")
	}
}

func TestPrepareUploadStreamsToServerOnlySpool(t *testing.T) {
	t.Parallel()
	budget, err := newSpoolBudget(2, 2*MaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("报销标准是 800 元。")
	prepared, err := prepareUpload(
		t.Context(), t.TempDir(), budget, `C:\\docs\\policy.md`, bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("prepareUpload: %v", err)
	}
	path := prepared.path
	if prepared.fileName != "policy.md" || prepared.mediaType != MediaTypeMarkdown {
		t.Fatalf("prepared metadata = %q, %q", prepared.fileName, prepared.mediaType)
	}
	if prepared.sizeBytes != int64(len(content)) {
		t.Fatalf("size = %d, want %d", prepared.sizeBytes, len(content))
	}
	wantHash := sha256.Sum256(content)
	if !bytes.Equal(prepared.rawSHA256, wantHash[:]) {
		t.Fatalf("hash = %x, want %x", prepared.rawSHA256, wantHash)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("spool mode = %o, want 600", info.Mode().Perm())
	}
	prepared.close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool still exists after close: %v", err)
	}
	if budget.active != 0 || budget.bytes != 0 {
		t.Fatalf("budget after close = active %d bytes %d", budget.active, budget.bytes)
	}
}

func TestPrepareUploadRejectsInvalidAndOversizedContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		fileName string
		content  []byte
		wantErr  error
	}{
		{"unsupported", "policy.csv", []byte("a,b"), ErrUnsupportedFileType},
		{"empty", "policy.txt", nil, ErrInvalidFile},
		{"invalid UTF-8", "policy.txt", []byte{0xff}, ErrInvalidFile},
		{"PDF signature", "policy.pdf", []byte("not a PDF"), ErrInvalidFile},
		{"DOCX container", "policy.docx", []byte("not a zip"), ErrInvalidFile},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			budget, err := newSpoolBudget(1, MaxFileBytes)
			if err != nil {
				t.Fatal(err)
			}
			_, err = prepareUpload(
				t.Context(), t.TempDir(), budget, test.fileName, bytes.NewReader(test.content),
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("prepareUpload error = %v, want %v", err, test.wantErr)
			}
			if budget.active != 0 || budget.bytes != 0 {
				t.Fatalf("failed upload leaked budget: active %d bytes %d", budget.active, budget.bytes)
			}
		})
	}

	budget, err := newSpoolBudget(1, MaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepareUpload(
		t.Context(), t.TempDir(), budget, "large.txt",
		strings.NewReader(strings.Repeat("x", MaxFileBytes+1)),
	)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized error = %v, want ErrFileTooLarge", err)
	}
}

func TestSpoolBudgetBoundsConcurrentFilesAndBytes(t *testing.T) {
	t.Parallel()
	budget, err := newSpoolBudget(2, MaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	first, err := budget.begin()
	if err != nil {
		t.Fatal(err)
	}
	second, err := budget.begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.begin(); !errors.Is(err, ErrSpoolCapacity) {
		t.Fatalf("third begin error = %v, want ErrSpoolCapacity", err)
	}
	if err := first.add(MaxFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := second.add(1); !errors.Is(err, ErrSpoolCapacity) {
		t.Fatalf("byte overflow error = %v, want ErrSpoolCapacity", err)
	}
	first.release()
	second.release()
}

func TestRawKeyRoundTrip(t *testing.T) {
	t.Parallel()
	fileID := uuid.Must(uuid.NewV7()).String()
	key, err := RawKey(fileID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FileIDFromRawKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if got != fileID {
		t.Fatalf("file ID = %q, want %q", got, fileID)
	}
	if _, err := FileIDFromRawKey("knowledge/files/not-a-uuid/source"); err == nil {
		t.Fatal("malformed raw key was accepted")
	}
}
