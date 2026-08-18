//go:build !windows

package docker

import (
	"fmt"
	"os"
	"testing"
)

func TestDockerProcessUser(t *testing.T) {
	if got := dockerProcessUser(true); got != "0:0" {
		t.Fatalf("rootless process user = %q, want 0:0", got)
	}
	if got, want := dockerProcessUser(false), fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()); got != want {
		t.Fatalf("rootful process user = %q, want %q", got, want)
	}
}
