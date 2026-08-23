package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEditDefinitionIncludesReplaceAllDefaultFalse(t *testing.T) {
	properties := editDefinition().InputSchema["properties"].(map[string]any)
	replaceAll, ok := properties["replace_all"].(map[string]any)
	if !ok {
		t.Fatal("edit schema is missing replace_all")
	}
	if got := replaceAll["type"]; got != "boolean" {
		t.Fatalf("replace_all type = %v, want boolean", got)
	}
	if got, ok := replaceAll["default"].(bool); !ok || got {
		t.Fatalf("replace_all default = %v, want false", replaceAll["default"])
	}
}

func TestEditUniquenessErrorReportsMatchesFromFile(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		oldString   string
		wantDetails []string
		wantAbsent  []string
	}{
		{
			name:      "matches on different lines",
			content:   "header\nneedle alpha\nmiddle\nneedle beta\nfooter\n",
			oldString: "needle",
			wantDetails: []string{
				"Showing first 2 of 2 matches:",
				"1. line 2, column 1: needle alpha",
				"2. line 4, column 1: needle beta",
				"replace_all: true to replace all 2 matches",
			},
		},
		{
			name:      "two matches on the same line",
			content:   "needle left needle right\n",
			oldString: "needle",
			wantDetails: []string{
				"Showing first 2 of 2 matches:",
				"1. line 1, column 1: needle left needle right",
				"2. line 1, column 13: needle left needle right",
			},
		},
		{
			name:      "multiline old string with trailing newline",
			content:   "header\nstart\nmiddle\nend\nseparator\nstart\nmiddle\nend\n",
			oldString: "start\nmiddle\n",
			wantDetails: []string{
				"1. line 2, column 1: start",
				"2. line 6, column 1: start",
			},
		},
		{
			name:      "CRLF multiline matches",
			content:   "header\r\nstart\r\nnext\r\nseparator\r\nstart\r\nnext\r\n",
			oldString: "start\r\nnext",
			wantDetails: []string{
				"1. line 2, column 1: start",
				"2. line 5, column 1: start",
			},
			wantAbsent: []string{"column 1: start\\r"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, tool := newEditTestFile(t, tt.content)
			_, err := tool.Execute(context.Background(), map[string]any{
				"path":       path,
				"old_string": tt.oldString,
				"new_string": "replacement",
			})
			if err == nil {
				t.Fatal("edit succeeded with a non-unique old_string")
			}
			message := err.Error()
			for _, want := range tt.wantDetails {
				if !strings.Contains(message, want) {
					t.Errorf("error missing %q:\n%s", want, message)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(message, absent) {
					t.Errorf("error unexpectedly contains %q:\n%s", absent, message)
				}
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != tt.content {
				t.Fatalf("failed uniqueness check changed file to %q", got)
			}
		})
	}
}

func TestEditCRLFLineNumbersAlignWithReadOffsets(t *testing.T) {
	content := "header\r\nstart\r\nnext\r\nseparator\r\nstart\r\nnext\r\n"
	path, tool := newEditTestFile(t, content)
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":       path,
		"old_string": "start\r\nnext",
		"new_string": "replacement",
	})
	if err == nil || !strings.Contains(err.Error(), "2. line 5, column 1: start") {
		t.Fatalf("edit error = %v, want second match on line 5", err)
	}

	got, err := newReadTool(&stubHost{}).Execute(context.Background(), map[string]any{
		"path":   path,
		"offset": 5,
		"limit":  1,
	})
	if err != nil {
		t.Fatalf("read line 5: %v", err)
	}
	if !strings.HasPrefix(got, "start\r\n") || strings.Contains(got, "next") {
		t.Fatalf("read offset 5 = %q, want only the CRLF start line before any pagination hint", got)
	}
}

func TestEditUniquenessErrorBoundsMatchListAndPreview(t *testing.T) {
	t.Run("lists only first five matches", func(t *testing.T) {
		var content strings.Builder
		for i := 1; i <= 7; i++ {
			fmt.Fprintf(&content, "match %d: needle\n", i)
		}
		path, tool := newEditTestFile(t, content.String())
		_, err := tool.Execute(context.Background(), map[string]any{
			"path":       path,
			"old_string": "needle",
			"new_string": "replacement",
		})
		if err == nil {
			t.Fatal("edit succeeded with seven matches")
		}
		message := err.Error()
		for _, want := range []string{
			"old_string matches 7 times",
			"Showing first 5 of 7 matches:",
			"5. line 5, column 10: match 5: needle",
			"replace_all: true to replace all 7 matches",
		} {
			if !strings.Contains(message, want) {
				t.Errorf("error missing %q:\n%s", want, message)
			}
		}
		for _, absent := range []string{"match 6: needle", "match 7: needle", "6. line"} {
			if strings.Contains(message, absent) {
				t.Errorf("bounded error unexpectedly contains %q:\n%s", absent, message)
			}
		}
	})

	t.Run("truncates long previews to fixed width", func(t *testing.T) {
		line := "needle" + strings.Repeat("界", 150)
		path, tool := newEditTestFile(t, line+"\n"+line+"\n")
		_, err := tool.Execute(context.Background(), map[string]any{
			"path":       path,
			"old_string": "needle",
			"new_string": "replacement",
		})
		if err == nil {
			t.Fatal("edit succeeded with two long-line matches")
		}
		previewPrefix := "1. line 1, column 1: "
		start := strings.Index(err.Error(), previewPrefix)
		if start < 0 {
			t.Fatalf("error missing first preview:\n%s", err)
		}
		preview := err.Error()[start+len(previewPrefix):]
		preview = strings.SplitN(preview, "\n", 2)[0]
		if got := utf8.RuneCountInString(preview); got != editMatchPreviewWidth {
			t.Fatalf("preview width = %d runes, want %d: %q", got, editMatchPreviewWidth, preview)
		}
		if !strings.HasSuffix(preview, "…") {
			t.Fatalf("truncated preview has no ellipsis: %q", preview)
		}
		if strings.Contains(preview, strings.Repeat("界", 120)) {
			t.Fatalf("preview retained content beyond the fixed width: %q", preview)
		}
	})
}

func TestEditReplaceAllReplacesEveryOccurrenceAndReportsCount(t *testing.T) {
	t.Run("multiple non-overlapping occurrences", func(t *testing.T) {
		path, tool := newEditTestFile(t, "np.int first\nnp.int second np.int\n")
		result, err := tool.Execute(context.Background(), map[string]any{
			"path":        path,
			"old_string":  "np.int",
			"new_string":  "np.int_",
			"replace_all": true,
		})
		if err != nil {
			t.Fatalf("replace all: %v", err)
		}
		if want := "replaced 3 occurrences"; !strings.Contains(result, want) {
			t.Fatalf("result = %q, missing %q", result, want)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "np.int_ first\nnp.int_ second np.int_\n"
		if string(got) != want {
			t.Fatalf("file content = %q, want %q", got, want)
		}
	})

	t.Run("overlapping candidates count like strings replace", func(t *testing.T) {
		path, tool := newEditTestFile(t, "aaa")
		result, err := tool.Execute(context.Background(), map[string]any{
			"path":        path,
			"old_string":  "aa",
			"new_string":  "x",
			"replace_all": true,
		})
		if err != nil {
			t.Fatalf("replace overlapping candidate: %v", err)
		}
		if want := "replaced 1 occurrence"; !strings.Contains(result, want) {
			t.Fatalf("result = %q, missing %q", result, want)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "xa" {
			t.Fatalf("file content = %q, want %q", got, "xa")
		}
	})
}

func newEditTestFile(t *testing.T, content string) (string, *hostEditTool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "edit.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, newEditTool(&stubHost{}).(*hostEditTool)
}
