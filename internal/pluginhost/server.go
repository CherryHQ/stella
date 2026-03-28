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

// ChannelRuntime is the minimal contract a channel implementation must satisfy
// to be served over the subprocess plugin protocol.
type ChannelRuntime interface {
	Start(ctx context.Context) error
	Notify(ctx context.Context, n pluginapi.ChannelNotification) error
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

// ServeChannel runs a channel plugin protocol loop on the given streams.
func ServeChannel(ctx context.Context, def Definition, runtime ChannelRuntime, in io.Reader, out io.Writer) error {
	runtimeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reqCh := make(chan pluginapi.Envelope, 8)
	readErrCh := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(in)
		for {
			env, err := readEnvelope(reader)
			if err != nil {
				readErrCh <- err
				close(reqCh)
				return
			}
			reqCh <- env
		}
	}()

	runErrCh := make(chan error, 1)
	go func() {
		if err := runtime.Start(runtimeCtx); err != nil && runtimeCtx.Err() == nil {
			runErrCh <- err
			return
		}
		runErrCh <- nil
	}()

	for {
		select {
		case err := <-runErrCh:
			if err != nil {
				return err
			}
			if runtimeCtx.Err() != nil {
				return runtimeCtx.Err()
			}
			return nil

		case err := <-readErrCh:
			if err != nil {
				if runtimeCtx.Err() != nil {
					return runtimeCtx.Err()
				}
				return err
			}

		case env, ok := <-reqCh:
			if !ok {
				if runtimeCtx.Err() != nil {
					return runtimeCtx.Err()
				}
				return nil
			}

			switch env.Method {
			case "handshake":
				result := pluginapi.HandshakeResponse{
					ProtocolVersion: pluginapi.ProtocolVersion,
					Name:            def.Manifest.Name,
					Version:         def.Manifest.Version,
					Kind:            def.Manifest.Kind,
					Capabilities:    def.Manifest.Capabilities,
				}
				if err := writeResponse(out, env.ID, result); err != nil {
					return err
				}

			case "health":
				if err := writeResponse(out, env.ID, pluginapi.HealthResponse{OK: runtimeCtx.Err() == nil}); err != nil {
					return err
				}

			case "start_channel":
				if err := writeResponse(out, env.ID, pluginapi.ChannelStartResponse{Started: true}); err != nil {
					return err
				}

			case "notify":
				var req pluginapi.ChannelNotifyRequest
				if err := json.Unmarshal(env.Params, &req); err != nil {
					if err := writeError(out, env.ID, "bad_request", fmt.Sprintf("decode notify request: %v", err)); err != nil {
						return err
					}
					continue
				}

				resp := pluginapi.ChannelNotifyResponse{Delivered: true}
				if err := runtime.Notify(runtimeCtx, req.Notification); err != nil {
					resp.Delivered = false
					resp.Error = err.Error()
				}
				if err := writeResponse(out, env.ID, resp); err != nil {
					return err
				}

			case "stop_channel", "shutdown":
				cancel()
				if err := writeResponse(out, env.ID, pluginapi.ChannelStopResponse{Stopped: true}); err != nil {
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
