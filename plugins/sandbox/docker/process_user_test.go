//go:build !windows

package docker

import (
	"fmt"
	"os"
	"testing"
)

func TestDockerProcessUser(t *testing.T) {
	currentUser := fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
	tests := []struct {
		name     string
		rootless bool
		mode     DockerSandboxMode
		want     string
	}{
		{name: "rootless host mount", rootless: true, mode: DockerSandboxModeHost, want: "0:0"},
		{name: "rootless bind mount", rootless: true, mode: DockerSandboxModeBind, want: "0:0"},
		{name: "rootless named volume", rootless: true, mode: DockerSandboxModeVolume, want: currentUser},
		{name: "rootful named volume", mode: DockerSandboxModeVolume, want: currentUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dockerProcessUser(tt.rootless, tt.mode); got != tt.want {
				t.Fatalf("dockerProcessUser(%v, %q) = %q, want %q", tt.rootless, tt.mode, got, tt.want)
			}
		})
	}
}
