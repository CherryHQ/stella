package agentpackage

import (
	"regexp"
	"strings"
	"testing"
)

var exportedToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func TestExportedToolNameAcceptsPeriodAndMaximumLengthPackageNames(t *testing.T) {
	for _, packageName := range []string{"acme.tools", strings.Repeat("a", 64)} {
		name, err := ExportedToolName(packageName, "remote", "list_items")
		if err != nil {
			t.Fatalf("ExportedToolName(%q): %v", packageName, err)
		}
		assertExportedToolName(t, name)
	}
}

func TestExportedToolNameRejectsInvalidPackageAndEmptyRemoteParts(t *testing.T) {
	for _, packageName := range []string{"", "Upper", "has/slash", "has..period", strings.Repeat("a", 65)} {
		if _, err := ExportedToolName(packageName, "remote", "list"); err == nil {
			t.Errorf("invalid package %q accepted", packageName)
		}
	}
	for _, parts := range [][2]string{{"", "list"}, {"remote", ""}} {
		if _, err := ExportedToolName("valid.package", parts[0], parts[1]); err == nil {
			t.Errorf("empty remote part %q/%q accepted", parts[0], parts[1])
		}
	}
}

func TestExportedToolNameAdaptsUnicodeRemoteNames(t *testing.T) {
	name, err := ExportedToolName("valid.package", "远程服务器", "列出项目")
	if err != nil {
		t.Fatal(err)
	}
	assertExportedToolName(t, name)
}

func TestExportedToolNameGolden(t *testing.T) {
	got, err := ExportedToolName("acme.tools", "remote", "list_items")
	if err != nil {
		t.Fatal(err)
	}
	const want = "acme_tools_remote_list_items_6b2474a68267"
	if got != want {
		t.Fatalf("ExportedToolName() = %q, want %q", got, want)
	}
}

func TestExportedToolNameSeparatesTupleBoundaries(t *testing.T) {
	// Both tuples sanitize to the same readable prefix; their complete tuple
	// hashes must still keep the exported names distinct.
	first, err := ExportedToolName("acme", "remote_tool", "list")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExportedToolName("acme", "remote", "tool_list")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("different MCP tuples mapped to the same name: %q", first)
	}
}

func TestExportedToolNameIsStableAndBounded(t *testing.T) {
	packageName := strings.Repeat("a", 64)
	serverKey := strings.Repeat("remote-", 100)
	localToolName := strings.Repeat("tool_", 100)
	first, err := ExportedToolName(packageName, serverKey, localToolName)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExportedToolName(packageName, serverKey, localToolName)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("mapping is unstable: %q != %q", first, second)
	}
	assertExportedToolName(t, first)
	if len(first) != maxExportedToolNameBytes {
		t.Fatalf("name length = %d, want %d at the truncation boundary", len(first), maxExportedToolNameBytes)
	}
}

func TestExportedToolNameRejectsInvalidUTF8(t *testing.T) {
	invalid := string([]byte{0xff})
	for _, tuple := range [][3]string{
		{invalid, "server", "tool"},
		{"valid.package", invalid, "tool"},
		{"valid.package", "server", invalid},
	} {
		if _, err := ExportedToolName(tuple[0], tuple[1], tuple[2]); err == nil {
			t.Errorf("invalid UTF-8 tuple %q accepted", tuple)
		}
	}
}

func assertExportedToolName(t *testing.T, name string) {
	t.Helper()
	if !exportedToolNamePattern.MatchString(name) {
		t.Fatalf("name %q contains a character outside [A-Za-z0-9_-]", name)
	}
}
