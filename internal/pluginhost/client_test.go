package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/pluginapi"
)

func TestClientHandshakeAndHealth(t *testing.T) {
	def := testDefinition(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Start(ctx, def, StartOptions{})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Health(ctx); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
}

func TestSupervisorRestartsDeadPlugin(t *testing.T) {
	def := testDefinition(t)
	supervisor := NewSupervisor(def, SupervisorOptions{RestartDelay: time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := supervisor.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	restarted, err := supervisor.EnsureHealthy(ctx)
	if err != nil {
		t.Fatalf("EnsureHealthy() error = %v", err)
	}
	defer func() { _ = supervisor.Close() }()

	if restarted == nil || !restarted.Alive() {
		t.Fatal("expected restarted client to be alive")
	}
	if got := supervisor.RestartCount(); got < 1 {
		t.Fatalf("RestartCount() = %d, want >= 1", got)
	}
}

func TestHelperPluginProcess(t *testing.T) {
	if os.Getenv("ANNA_PLUGIN_HELPER_PROCESS") != "1" {
		return
	}

	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var env pluginapi.Envelope
		if err := decoder.Decode(&env); err != nil {
			return
		}

		switch env.Method {
		case "handshake":
			writeResponse(t, encoder, env.ID, pluginapi.HandshakeResponse{
				ProtocolVersion: pluginapi.ProtocolVersion,
				Name:            "helper",
				Version:         "1.0.0",
				Kind:            pluginapi.KindTool,
				Capabilities: []pluginapi.Capability{
					pluginapi.CapabilityHealthCheck,
					pluginapi.CapabilityGracefulShutdown,
				},
			})
		case "health":
			writeResponse(t, encoder, env.ID, pluginapi.HealthResponse{OK: true})
		case "shutdown":
			writeResponse(t, encoder, env.ID, struct{}{})
			return
		default:
			_ = encoder.Encode(pluginapi.Envelope{
				ID:   env.ID,
				Type: pluginapi.MessageTypeResponse,
				Error: &pluginapi.RPCError{
					Code:    "unknown_method",
					Message: env.Method,
				},
			})
		}
	}
}

func testDefinition(t *testing.T) Definition {
	t.Helper()
	root := t.TempDir()
	entry := filepath.Join(root, "helper.sh")
	script := fmt.Sprintf("#!/bin/sh\nANNA_PLUGIN_HELPER_PROCESS=1 exec %q -test.run TestHelperPluginProcess --\n", os.Args[0])
	if err := os.WriteFile(entry, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, ManifestFilename)
	writeManifest(t, manifestPath, pluginapi.Manifest{
		Name:            "helper",
		Version:         "1.0.0",
		Kind:            pluginapi.KindTool,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      "helper.sh",
	})
	def, err := LoadDefinition(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func writeResponse(t *testing.T, encoder *json.Encoder, id string, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(pluginapi.Envelope{
		ID:     id,
		Type:   pluginapi.MessageTypeResponse,
		Result: raw,
	}); err != nil {
		t.Fatal(err)
	}
}
