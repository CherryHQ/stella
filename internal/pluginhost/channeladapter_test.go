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

func TestChannelAdapterRestartsAfterCrash(t *testing.T) {
	def := testChannelDefinitionWithEnv(t, map[string]string{
		"ANNA_PLUGIN_HELPER_CHANNEL_EXIT_ON_NOTIFY": "1",
	})

	adapter := NewChannelAdapter(def, SupervisorOptions{RestartDelay: time.Millisecond})
	defer func() { _ = adapter.supervisor.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := adapter.Notify(ctx, pluginapi.ChannelNotification{Text: "first"}); err != nil {
		t.Fatalf("Notify() first error = %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if err := adapter.Notify(ctx, pluginapi.ChannelNotification{Text: "second"}); err != nil {
		t.Fatalf("Notify() second error = %v", err)
	}

	if got := adapter.supervisor.RestartCount(); got < 1 {
		t.Fatalf("RestartCount() = %d, want >= 1", got)
	}
}

func TestHelperChannelPluginProcess(t *testing.T) {
	if os.Getenv("ANNA_PLUGIN_HELPER_CHANNEL_PROCESS") != "1" {
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
			writeTestChannelResponse(t, encoder, env.ID, pluginapi.HandshakeResponse{
				ProtocolVersion: pluginapi.ProtocolVersion,
				Name:            "helper-channel",
				Version:         "1.0.0",
				Kind:            pluginapi.KindChannel,
				Capabilities: []pluginapi.Capability{
					pluginapi.CapabilityChannelStart,
					pluginapi.CapabilityChannelStop,
					pluginapi.CapabilityChannelNotify,
					pluginapi.CapabilityChannelInbound,
					pluginapi.CapabilityHealthCheck,
					pluginapi.CapabilityGracefulShutdown,
				},
			})
		case "health":
			writeTestChannelResponse(t, encoder, env.ID, pluginapi.HealthResponse{OK: true})
		case "notify":
			var req pluginapi.ChannelNotifyRequest
			if err := json.Unmarshal(env.Params, &req); err != nil {
				writeTestChannelResponse(t, encoder, env.ID, pluginapi.ChannelNotifyResponse{Delivered: false, Error: err.Error()})
				continue
			}
			writeTestChannelResponse(t, encoder, env.ID, pluginapi.ChannelNotifyResponse{Delivered: true})
			if os.Getenv("ANNA_PLUGIN_HELPER_CHANNEL_EXIT_ON_NOTIFY") == "1" {
				return
			}
		case "shutdown", "stop_channel":
			writeTestChannelResponse(t, encoder, env.ID, pluginapi.ChannelStopResponse{Stopped: true})
			return
		default:
			writeTestChannelResponse(t, encoder, env.ID, pluginapi.ChannelNotifyResponse{Delivered: false, Error: env.Method})
		}
	}
}

func testChannelDefinitionWithEnv(t *testing.T, extraEnv map[string]string) Definition {
	t.Helper()
	root := t.TempDir()
	entry := filepath.Join(root, "helper.sh")
	var envPrefix string
	for k, v := range extraEnv {
		envPrefix += fmt.Sprintf("%s=%q ", k, v)
	}
	script := fmt.Sprintf("#!/bin/sh\nANNA_PLUGIN_HELPER_CHANNEL_PROCESS=1 %sexec %q -test.run TestHelperChannelPluginProcess --\n", envPrefix, os.Args[0])
	if err := os.WriteFile(entry, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, ManifestFilename)
	writeManifest(t, manifestPath, pluginapi.Manifest{
		Name:            "helper-channel",
		Version:         "1.0.0",
		Kind:            pluginapi.KindChannel,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      "helper.sh",
		Capabilities: []pluginapi.Capability{
			pluginapi.CapabilityChannelStart,
			pluginapi.CapabilityChannelStop,
			pluginapi.CapabilityChannelNotify,
			pluginapi.CapabilityChannelInbound,
			pluginapi.CapabilityHealthCheck,
			pluginapi.CapabilityGracefulShutdown,
		},
	})
	def, err := LoadDefinition(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func writeTestChannelResponse(t *testing.T, encoder *json.Encoder, id string, payload any) {
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
