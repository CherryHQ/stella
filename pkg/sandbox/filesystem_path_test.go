package sandbox

import "testing"

func TestIsCanonicalFilesystemPath(t *testing.T) {
	for _, value := range []string{"/workspace", "/workspace/a", "/user/assets/a", "/tmp/x"} {
		if !IsCanonicalFilesystemPath(value) {
			t.Errorf("accepted canonical path %q", value)
		}
	}
	for _, value := range []string{"workspace/a", "/workspace/../user/a", "/workspace//a", `/workspace\a`, "/workspace-else/a", "/etc/passwd"} {
		if IsCanonicalFilesystemPath(value) {
			t.Errorf("accepted noncanonical/outside path %q", value)
		}
	}
}
