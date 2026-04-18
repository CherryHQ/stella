package docker

import (
	"testing"
)

func TestConfigImageOrDefault(t *testing.T) {
	if got := (Config{}).ImageOrDefault(); got != "alpine:3.20" {
		t.Errorf("empty Config.ImageOrDefault() = %q, want %q", got, "alpine:3.20")
	}
	if got := (Config{Image: "ubuntu:22.04"}).ImageOrDefault(); got != "ubuntu:22.04" {
		t.Errorf("Config{Image: ubuntu:22.04}.ImageOrDefault() = %q, want %q", got, "ubuntu:22.04")
	}
}

func TestConfigWorkspaceMountOrDefault(t *testing.T) {
	if got := (Config{}).WorkspaceMountOrDefault(); got != "/workspace" {
		t.Errorf("empty Config.WorkspaceMountOrDefault() = %q, want %q", got, "/workspace")
	}
	if got := (Config{WorkspaceMount: "/custom"}).WorkspaceMountOrDefault(); got != "/custom" {
		t.Errorf("Config{WorkspaceMount: /custom}.WorkspaceMountOrDefault() = %q, want %q", got, "/custom")
	}
}

func TestParseExtraMounts_Valid(t *testing.T) {
	cases := []struct {
		input    string
		wantHost string
		wantCont string
		wantRO   bool
	}{
		{"/host/data:/data", "/host/data", "/data", false},
		{"/host/lib:/container/lib:ro", "/host/lib", "/container/lib", true},
		{"/host/rw:/container/rw:rw", "/host/rw", "/container/rw", false},
	}

	for _, tc := range cases {
		mounts, err := parseExtraMounts([]string{tc.input})
		if err != nil {
			t.Errorf("parseExtraMounts(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if len(mounts) != 1 {
			t.Errorf("parseExtraMounts(%q): expected 1 mount, got %d", tc.input, len(mounts))
			continue
		}
		m := mounts[0]
		if m.HostPath != tc.wantHost {
			t.Errorf("parseExtraMounts(%q): HostPath = %q, want %q", tc.input, m.HostPath, tc.wantHost)
		}
		if m.ContainerPath != tc.wantCont {
			t.Errorf("parseExtraMounts(%q): ContainerPath = %q, want %q", tc.input, m.ContainerPath, tc.wantCont)
		}
		if m.ReadOnly != tc.wantRO {
			t.Errorf("parseExtraMounts(%q): ReadOnly = %v, want %v", tc.input, m.ReadOnly, tc.wantRO)
		}
	}
}

func TestParseExtraMounts_Invalid(t *testing.T) {
	invalid := []string{
		"/only/one/part",
		"host:container:bad_option",
		":/container",
		"/host:",
		"",
	}
	for _, tc := range invalid {
		_, err := parseExtraMounts([]string{tc})
		if err == nil {
			t.Errorf("parseExtraMounts(%q): expected error, got nil", tc)
		}
	}
}
