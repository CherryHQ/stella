//go:build darwin

package main

import (
	"strings"
	"testing"
)

func TestLaunchAgentOwnsDockerSandboxMode(t *testing.T) {
	if !strings.Contains(plistTemplate, "<key>STELLA_DOCKER_SANDBOX_MODE</key>\n        <string>host</string>") {
		t.Fatal("LaunchAgent must pin native Docker sandbox mode to host")
	}
}
