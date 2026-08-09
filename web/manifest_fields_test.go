package web

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/manifestplugins"
)

// The editor names the definition fields a save takes ownership of, and the
// server refuses any name it does not recognise. That makes a stray name a loud
// 400 — but a *missing* one is silent: the field is edited, never claimed, and
// goes back to following the shipped definition on the next upgrade, which is
// the exact bug sparse overrides exist to prevent. Go derives its list from the
// definition struct; the editor has to spell it out, so pin the two together.
func TestEditorKnowsEveryOwnableDefinitionField(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("src", "features", "plugins", "pluginUtils.ts"))
	if err != nil {
		t.Fatalf("read pluginUtils.ts: %v", err)
	}
	block := regexp.MustCompile(`manifestPluginDefinitionFields\s*=\s*\[([^\]]*)\]`).FindSubmatch(source)
	if block == nil {
		t.Fatal("manifestPluginDefinitionFields not found in pluginUtils.ts")
	}
	var editor []string
	for _, quoted := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		editor = append(editor, string(quoted[1]))
	}

	server := manifestplugins.OwnableFields()
	slices.Sort(editor)
	slices.Sort(server)
	if !slices.Equal(editor, server) {
		t.Errorf("the editor claims ownership of %s, the server allows %s — a field the editor omits is edited but never owned",
			strings.Join(editor, ","), strings.Join(server, ","))
	}
}
