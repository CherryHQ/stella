package tool

import (
	"os"
	"strings"
	"testing"
)

func TestWrapWithRTK_NoRTK(t *testing.T) {
	// Save and restore the cached rtkPath.
	orig := rtkPath
	rtkPath = func() string { return "" }
	defer func() { rtkPath = orig }()

	cmd := "git status"
	if got := wrapWithRTK(cmd); got != cmd {
		t.Errorf("wrapWithRTK(%q) = %q, want original when rtk absent", cmd, got)
	}
}

func TestEnvWithToolsBin_PrependsPATH(t *testing.T) {
	env := envWithToolsBin()

	var pathEntry string
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			pathEntry = e
			break
		}
	}
	if pathEntry == "" {
		t.Fatal("PATH not found in env")
	}

	// ANNA_HOME/bin should be the first entry.
	parts := strings.SplitN(pathEntry[5:], string(os.PathListSeparator), 2)
	if !strings.HasSuffix(parts[0], "bin") {
		t.Errorf("first PATH entry should end with bin, got %q", parts[0])
	}
}
