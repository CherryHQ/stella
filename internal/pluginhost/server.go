package pluginhost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/vaayne/anna/internal/pluginapi"
)

// ToolRuntime is the minimal contract a tool implementation must satisfy to be
// served over the subprocess plugin protocol.
type ToolRuntime interface {
	Execute(ctx context.Context, args map[string]any) (string, error)
}

// ServeTool runs a single-tool plugin protocol loop on the given streams.
func ServeTool(ctx context.Context, def Definition, runtime ToolRuntime, in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	for {
		env, err := readEnvelope(reader)
		if err != nil {
			return err
		}

		switch env.Method {
		case "handshake":
			result := pluginapi.HandshakeResponse{
				ProtocolVersion: pluginapi.ProtocolVersion,
				Name:            def.Manifest.Name,
				Version:         def.Manifest.Version,
				Kind:            def.Manifest.Kind,
				Capabilities:    def.Manifest.Capabilities,
				Tool:            def.Manifest.Tool,
			}
			if err := writeResponse(out, env.ID, result); err != nil {
				return err
			}

		case "health":
			if err := writeResponse(out, env.ID, pluginapi.HealthResponse{OK: true}); err != nil {
				return err
			}

		case "call_tool":
			var req pluginapi.ToolCallRequest
			if err := json.Unmarshal(env.Params, &req); err != nil {
				if err := writeError(out, env.ID, "bad_request", fmt.Sprintf("decode tool request: %v", err)); err != nil {
					return err
				}
				continue
			}

			result, err := runtime.Execute(ctx, req.Arguments)
			resp := pluginapi.ToolCallResponse{Output: result}
			if err != nil {
				resp.Error = err.Error()
			}
			if err := writeResponse(out, env.ID, resp); err != nil {
				return err
			}

		case "shutdown":
			if err := writeResponse(out, env.ID, struct{}{}); err != nil {
				return err
			}
			return nil

		default:
			if err := writeError(out, env.ID, "unknown_method", env.Method); err != nil {
				return err
			}
		}
	}
}

func writeResponse(w io.Writer, id string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	return writeEnvelope(w, pluginapi.Envelope{
		ID:     id,
		Type:   pluginapi.MessageTypeResponse,
		Result: data,
	})
}

func writeError(w io.Writer, id, code, message string) error {
	return writeEnvelope(w, pluginapi.Envelope{
		ID:   id,
		Type: pluginapi.MessageTypeResponse,
		Error: &pluginapi.RPCError{
			Code:    code,
			Message: message,
		},
	})
}
