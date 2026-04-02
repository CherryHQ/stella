package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/vaayne/anna/internal/pluginapi"
)

// stubToolRuntime is a minimal ToolRuntime for testing ServeTool.
type stubToolRuntime struct {
	output string
	err    error
}

func (s *stubToolRuntime) Execute(_ context.Context, _ map[string]any) (string, error) {
	return s.output, s.err
}

func sendRequests(reqs ...pluginapi.Envelope) io.Reader {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range reqs {
		_ = enc.Encode(r)
	}
	return &buf
}

func decodeResponses(r io.Reader) []pluginapi.Envelope {
	dec := json.NewDecoder(r)
	var out []pluginapi.Envelope
	for {
		var env pluginapi.Envelope
		if err := dec.Decode(&env); err != nil {
			break
		}
		out = append(out, env)
	}
	return out
}

func toolDef() Definition {
	return Definition{
		Manifest: pluginapi.Manifest{
			Name:            "test-tool",
			Version:         "0.1.0",
			Kind:            pluginapi.KindTool,
			ProtocolVersion: pluginapi.ProtocolVersion,
			Entrypoint:      "echo",
			Tool: &pluginapi.ToolSpec{
				Name:        "test-tool",
				Description: "a test tool",
			},
			Capabilities: []pluginapi.Capability{
				pluginapi.CapabilityToolCall,
				pluginapi.CapabilityHealthCheck,
				pluginapi.CapabilityGracefulShutdown,
			},
		},
	}
}

func channelDef() Definition {
	return Definition{
		Manifest: pluginapi.Manifest{
			Name:            "test-channel",
			Version:         "0.1.0",
			Kind:            pluginapi.KindChannel,
			ProtocolVersion: pluginapi.ProtocolVersion,
			Entrypoint:      "echo",
			Capabilities: []pluginapi.Capability{
				pluginapi.CapabilityChannelStart,
				pluginapi.CapabilityChannelNotify,
				pluginapi.CapabilityHealthCheck,
				pluginapi.CapabilityGracefulShutdown,
			},
		},
	}
}

func TestServeToolHandshakeHealthAndShutdown(t *testing.T) {
	in := sendRequests(
		pluginapi.Envelope{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		pluginapi.Envelope{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "health"},
		pluginapi.Envelope{ID: "3", Type: pluginapi.MessageTypeRequest, Method: "shutdown"},
	)
	var out bytes.Buffer

	err := ServeTool(context.Background(), toolDef(), &stubToolRuntime{output: "ok"}, in, &out)
	if err != nil {
		t.Fatalf("ServeTool() error = %v", err)
	}

	responses := decodeResponses(&out)
	if len(responses) != 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}
	if responses[0].Error != nil {
		t.Errorf("handshake error: %v", responses[0].Error)
	}
	var health pluginapi.HealthResponse
	_ = json.Unmarshal(responses[1].Result, &health)
	if !health.OK {
		t.Error("health response not OK")
	}
}

func TestServeToolCallTool(t *testing.T) {
	params, _ := json.Marshal(pluginapi.ToolCallRequest{Name: "test-tool", Arguments: map[string]any{"x": 1}})
	in := sendRequests(
		pluginapi.Envelope{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		pluginapi.Envelope{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "call_tool", Params: params},
		pluginapi.Envelope{ID: "3", Type: pluginapi.MessageTypeRequest, Method: "shutdown"},
	)
	var out bytes.Buffer

	err := ServeTool(context.Background(), toolDef(), &stubToolRuntime{output: "hello"}, in, &out)
	if err != nil {
		t.Fatalf("ServeTool() error = %v", err)
	}

	responses := decodeResponses(&out)
	if len(responses) != 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}
	var resp pluginapi.ToolCallResponse
	if err := json.Unmarshal(responses[1].Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Output != "hello" {
		t.Errorf("output = %q, want %q", resp.Output, "hello")
	}
}

func TestServeToolCallToolWithError(t *testing.T) {
	params, _ := json.Marshal(pluginapi.ToolCallRequest{Name: "test-tool", Arguments: map[string]any{}})
	in := sendRequests(
		pluginapi.Envelope{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		pluginapi.Envelope{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "call_tool", Params: params},
		pluginapi.Envelope{ID: "3", Type: pluginapi.MessageTypeRequest, Method: "shutdown"},
	)
	var out bytes.Buffer

	err := ServeTool(context.Background(), toolDef(), &stubToolRuntime{
		output: "",
		err:    errors.New("exec failed"),
	}, in, &out)
	if err != nil {
		t.Fatalf("ServeTool() error = %v", err)
	}

	responses := decodeResponses(&out)
	if len(responses) != 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}
	var resp pluginapi.ToolCallResponse
	if err := json.Unmarshal(responses[1].Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != "exec failed" {
		t.Errorf("error = %q, want %q", resp.Error, "exec failed")
	}
}

func TestServeToolCallToolBadParams(t *testing.T) {
	// Use valid JSON that doesn't match ToolCallRequest shape.
	in := sendRequests(
		pluginapi.Envelope{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		pluginapi.Envelope{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "call_tool", Params: json.RawMessage(`"not-an-object"`)},
		pluginapi.Envelope{ID: "3", Type: pluginapi.MessageTypeRequest, Method: "shutdown"},
	)
	var out bytes.Buffer

	err := ServeTool(context.Background(), toolDef(), &stubToolRuntime{}, in, &out)
	if err != nil {
		t.Fatalf("ServeTool() error = %v", err)
	}

	responses := decodeResponses(&out)
	if len(responses) != 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}
	if responses[1].Error == nil {
		t.Fatal("expected error for bad params")
	}
	if responses[1].Error.Code != "bad_request" {
		t.Errorf("code = %q, want %q", responses[1].Error.Code, "bad_request")
	}
}

func TestServeToolUnknownMethod(t *testing.T) {
	in := sendRequests(
		pluginapi.Envelope{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		pluginapi.Envelope{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "bogus"},
		pluginapi.Envelope{ID: "3", Type: pluginapi.MessageTypeRequest, Method: "shutdown"},
	)
	var out bytes.Buffer

	err := ServeTool(context.Background(), toolDef(), &stubToolRuntime{}, in, &out)
	if err != nil {
		t.Fatalf("ServeTool() error = %v", err)
	}

	responses := decodeResponses(&out)
	if responses[1].Error == nil || responses[1].Error.Code != "unknown_method" {
		t.Errorf("expected unknown_method error, got %v", responses[1].Error)
	}
}

// stubChannelRuntime is a minimal ChannelRuntime for testing ServeChannel.
type stubChannelRuntime struct {
	startErr  error
	notifyErr error
}

func (s *stubChannelRuntime) Start(ctx context.Context) error {
	if s.startErr != nil {
		return s.startErr
	}
	<-ctx.Done()
	return nil
}

func (s *stubChannelRuntime) Notify(_ context.Context, _ pluginapi.ChannelNotification) error {
	return s.notifyErr
}

// serveChannelPiped sends messages through a pipe so the reader goroutine
// blocks after all messages instead of returning EOF immediately.
func serveChannelPiped(t *testing.T, def Definition, rt ChannelRuntime, msgs []pluginapi.Envelope) []pluginapi.Envelope {
	t.Helper()

	pr, pw := io.Pipe()
	go func() {
		enc := json.NewEncoder(pw)
		for _, m := range msgs {
			_ = enc.Encode(m)
		}
		// Don't close — let ServeChannel exit via shutdown, then close to
		// unblock the reader goroutine.
	}()

	var out bytes.Buffer
	err := ServeChannel(context.Background(), def, rt, pr, &out)
	_ = pw.Close()
	if err != nil {
		t.Fatalf("ServeChannel() error = %v", err)
	}
	return decodeResponses(&out)
}

func TestServeChannelLifecycle(t *testing.T) {
	notifyParams, _ := json.Marshal(pluginapi.ChannelNotifyRequest{
		Notification: pluginapi.ChannelNotification{Text: "hi"},
	})

	responses := serveChannelPiped(t, channelDef(), &stubChannelRuntime{}, []pluginapi.Envelope{
		{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "health"},
		{ID: "3", Type: pluginapi.MessageTypeRequest, Method: "start_channel"},
		{ID: "4", Type: pluginapi.MessageTypeRequest, Method: "notify", Params: notifyParams},
		{ID: "5", Type: pluginapi.MessageTypeRequest, Method: "shutdown"},
	})

	if len(responses) != 5 {
		t.Fatalf("got %d responses, want 5", len(responses))
	}
	var notifyResp pluginapi.ChannelNotifyResponse
	_ = json.Unmarshal(responses[3].Result, &notifyResp)
	if !notifyResp.Delivered {
		t.Error("notify not delivered")
	}
}

func TestServeChannelNotifyWithError(t *testing.T) {
	notifyParams, _ := json.Marshal(pluginapi.ChannelNotifyRequest{
		Notification: pluginapi.ChannelNotification{Text: "hi"},
	})

	responses := serveChannelPiped(t, channelDef(), &stubChannelRuntime{notifyErr: io.ErrUnexpectedEOF}, []pluginapi.Envelope{
		{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "notify", Params: notifyParams},
		{ID: "3", Type: pluginapi.MessageTypeRequest, Method: "shutdown"},
	})

	if len(responses) != 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}
	var resp pluginapi.ChannelNotifyResponse
	_ = json.Unmarshal(responses[1].Result, &resp)
	if resp.Delivered {
		t.Error("expected not delivered")
	}
	if resp.Error == "" {
		t.Error("expected error message")
	}
}

func TestServeChannelBadNotifyParams(t *testing.T) {
	responses := serveChannelPiped(t, channelDef(), &stubChannelRuntime{}, []pluginapi.Envelope{
		{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "notify", Params: json.RawMessage(`"not-an-object"`)},
		{ID: "3", Type: pluginapi.MessageTypeRequest, Method: "shutdown"},
	})

	if len(responses) != 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}
	if responses[1].Error == nil || responses[1].Error.Code != "bad_request" {
		t.Errorf("expected bad_request, got %v", responses[1].Error)
	}
}

func TestServeChannelUnknownMethod(t *testing.T) {
	responses := serveChannelPiped(t, channelDef(), &stubChannelRuntime{}, []pluginapi.Envelope{
		{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "bogus"},
		{ID: "3", Type: pluginapi.MessageTypeRequest, Method: "shutdown"},
	})

	if len(responses) != 3 {
		t.Fatalf("got %d responses, want 3", len(responses))
	}
	if responses[1].Error == nil || responses[1].Error.Code != "unknown_method" {
		t.Errorf("expected unknown_method, got %v", responses[1].Error)
	}
}

func TestServeChannelStopChannel(t *testing.T) {
	responses := serveChannelPiped(t, channelDef(), &stubChannelRuntime{}, []pluginapi.Envelope{
		{ID: "1", Type: pluginapi.MessageTypeRequest, Method: "handshake"},
		{ID: "2", Type: pluginapi.MessageTypeRequest, Method: "stop_channel"},
	})

	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(responses))
	}
	var resp pluginapi.ChannelStopResponse
	_ = json.Unmarshal(responses[1].Result, &resp)
	if !resp.Stopped {
		t.Error("expected stopped")
	}
}
