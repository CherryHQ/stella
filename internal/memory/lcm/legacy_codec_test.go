package lcm

import (
	"encoding/json"
	"fmt"

	"github.com/CherryHQ/stella/pkg/ai"
)

// The pre-canonical encoders live here, in tests only. Rows written before
// canonical media still sit in every deployment's ctx_message, so the readers
// must keep restoring them; these builders produce that exact legacy shape so a
// reader test asserts against real bytes instead of hand-written JSON.

func legacyUserMessageToRows(m ai.UserMessage) []storageRow {
	switch c := m.Content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeText, content: c, tokenText: c}}
	case []ai.ContentBlock:
		data, err := json.Marshal(contentBlocksToJSON(c))
		if err != nil {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeMultimodal, content: string(data), tokenText: string(data)}}
	default:
		s := fmt.Sprintf("%v", m.Content)
		if s == "" {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeText, content: s, tokenText: s}}
	}
}

func legacyToolResultToRows(m ai.ToolResultMessage) []storageRow {
	// Runner is the single extraction chokepoint, so the common path arrives with
	// references already on the message and a clean body; scrubRenderableRefs is a
	// no-op there. The fallback only fires for a legacy/direct tool result that
	// reached memory with a raw sentinel still in some text block — per block, so
	// the cleaning also covers the image path's Blocks below, not just the text.
	content, fallbackRefs := scrubRenderableRefs(m.Content)
	refs := mergeReferences(m.References, fallbackRefs)
	text := ai.FlattenText(content)
	resultJSON, _ := json.Marshal(text)
	var errStr string
	if m.IsError {
		errStr = text
	}
	envelope := toolResultEnvelope{
		ID:             m.ToolCallID,
		Tool:           m.ToolName,
		Result:         resultJSON,
		Error:          errStr,
		IsError:        m.IsError,
		ErrorKind:      m.ErrorKind,
		References:     refs,
		ChildToolCalls: m.ChildToolCalls,
	}
	if ai.HasImage(content) {
		envelope.Blocks = contentBlocksToJSON(content)
	}
	data, _ := json.Marshal(envelope)
	return []storageRow{{role: roleTool, eventType: eventTypeToolResult, content: string(data), tokenText: string(data)}}
}
