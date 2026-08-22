package hooks

import (
	"strings"
	"testing"
)

func TestRedactToolTextMasksGitHubFineGrainedAndQuotedSecrets(t *testing.T) {
	for _, input := range []string{
		`github_pat_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ`,
		`token="github_pat_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"`,
	} {
		got := RedactToolText(input)
		if strings.Contains(got, "github_pat_") || !strings.Contains(got, "[REDACTED]") {
			t.Fatalf("RedactToolText(%q) = %q", input, got)
		}
	}
}
