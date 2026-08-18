package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/channel"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type webGroupPublisher struct {
	w          http.ResponseWriter
	flusher    http.Flusher
	clientGone bool
}

func streamEmptyGroupReply(w http.ResponseWriter, flusher http.Flusher) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Vercel-AI-UI-Message-Stream", "v1")
	w.WriteHeader(http.StatusOK)
	p := &webGroupPublisher{w: w, flusher: flusher}
	p.writeSSE(map[string]string{"type": "start", "messageId": uuid.Must(uuid.NewV7()).String()})
	p.writeSSE(map[string]string{"type": "finish"})
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (p *webGroupPublisher) writeSSE(v any) {
	if p.clientGone {
		return
	}
	data, _ := json.Marshal(v)
	if _, err := fmt.Fprintf(p.w, "data: %s\n\n", data); err != nil {
		p.clientGone = true
		return
	}
	p.flusher.Flush()
}

func (p *webGroupPublisher) Publish(ctx context.Context, req channel.GroupPublishRequest) error {
	stream, err := channel.ValidateGroupReplay(ctx, req.Stream)
	if err != nil {
		return err
	}
	if stream == nil {
		return nil
	}
	p.writeSSE(map[string]string{"type": "start-step"})
	defer p.writeSSE(map[string]string{"type": "finish-step"})

	p.writeSSE(map[string]any{
		"type": "data-agent-info",
		"id":   uuid.Must(uuid.NewV7()).String(),
		"data": map[string]any{"agentId": req.AgentID, "agentName": req.AgentName},
	})

	var (
		inText      bool
		textID      string
		inReasoning bool
		reasoningID string
	)
	closeText := func() {
		if inText {
			p.writeSSE(map[string]string{"type": "text-end", "id": textID})
			inText = false
		}
	}
	closeReasoning := func() {
		if inReasoning {
			p.writeSSE(map[string]string{"type": "reasoning-end", "id": reasoningID})
			inReasoning = false
		}
	}
	closeOpenParts := func() {
		closeText()
		closeReasoning()
	}

	for {
		select {
		case evt, ok := <-stream.Events:
			if !ok {
				closeOpenParts()
				return nil
			}
			if evt.Err != nil {
				closeOpenParts()
				return evt.Err
			}
			if evt.Reasoning != "" {
				closeText()
				if !inReasoning {
					reasoningID = uuid.Must(uuid.NewV7()).String()
					p.writeSSE(map[string]string{"type": "reasoning-start", "id": reasoningID})
					inReasoning = true
				}
				p.writeSSE(map[string]any{"type": "reasoning-delta", "id": reasoningID, "delta": evt.Reasoning})
				continue
			}
			if evt.Text != "" {
				closeReasoning()
				if !inText {
					textID = uuid.Must(uuid.NewV7()).String()
					p.writeSSE(map[string]string{"type": "text-start", "id": textID})
					inText = true
				}
				p.writeSSE(map[string]any{"type": "text-delta", "id": textID, "delta": evt.Text})
				continue
			}
			if evt.ToolUse != nil {
				closeOpenParts()
				p.writeToolUse(evt.ToolUse)
				continue
			}
			if evt.Image != nil {
				closeOpenParts()
				dataURI := "data:" + evt.Image.MimeType + ";base64," + evt.Image.Data
				p.writeSSE(map[string]string{"type": "file", "url": dataURI, "mediaType": evt.Image.MimeType})
				continue
			}
			if evt.File != nil {
				closeOpenParts()
				fileURL := fmt.Sprintf("/api/agents/%s/sessions/%s/workspace/file-content?path=%s&raw=true",
					req.AgentID, req.Stream.SessionID, url.QueryEscape(evt.File.Path))
				p.writeSSE(map[string]string{"type": "file", "url": fileURL, "mediaType": detectMIME(evt.File.Name)})
				continue
			}
		case <-ctx.Done():
			closeOpenParts()
			return ctx.Err()
		}
	}
}

func (p *webGroupPublisher) writeToolUse(tu *pkgchannel.ToolUseEvent) {
	toolCallID := tu.ID
	if toolCallID == "" {
		toolCallID = uuid.Must(uuid.NewV7()).String()
	}
	switch tu.Status {
	case "running":
		p.writeSSE(map[string]any{
			"type":       "tool-input-start",
			"toolCallId": toolCallID,
			"toolName":   tu.Tool,
			"dynamic":    true,
		})
		args := tu.Arguments
		if args == nil {
			args = map[string]any{"input": tu.Input}
		}
		p.writeSSE(map[string]any{
			"type":       "tool-input-available",
			"toolCallId": toolCallID,
			"toolName":   tu.Tool,
			"dynamic":    true,
			"input":      args,
		})
	case "done":
		output := tu.Content
		if output == "" {
			output = tu.Detail
		}
		p.writeSSE(map[string]any{"type": "tool-output-available", "toolCallId": toolCallID, "output": output})
	case "error":
		errorText := tu.Content
		if errorText == "" {
			errorText = tu.Detail
		}
		p.writeSSE(map[string]any{"type": "tool-output-error", "toolCallId": toolCallID, "errorText": errorText})
	}
}
