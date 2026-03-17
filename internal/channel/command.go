package channel

import (
	"context"
	"fmt"
	"strings"
)

// WelcomeMessage is the shared welcome/help text for all channels.
const WelcomeMessage = "Hi! I'm Anna -- your local AI assistant.\n\n" +
	"Commands:\n" +
	"/new -- Start a fresh session\n" +
	"/compact -- Compress conversation history\n" +
	"/model -- Switch between models\n" +
	"/agent -- List or switch agents\n" +
	"/whoami -- Show your user ID\n\n" +
	"Just send me a message to get started."

// HandleCommand processes common bot commands shared across all channels.
// Returns the response text and whether the command was handled.
// /model and /agent are left to each channel (they need platform-specific UI).
func HandleCommand(ctx context.Context, rc *ResolvedChat, text, senderID string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(fields[0])

	switch cmd {
	case "/start", "/help":
		return WelcomeMessage, true

	case "/new":
		info, err := rc.RotateSession()
		if err != nil {
			return fmt.Sprintf("Error creating new session: %v", err), true
		}
		_ = info
		return "New session started.", true

	case "/compact":
		_, err := rc.CompactSession(ctx)
		if err != nil {
			return fmt.Sprintf("Compaction failed: %v", err), true
		}
		return "Session compacted.", true

	case "/whoami":
		return fmt.Sprintf("Your ID: %s", senderID), true
	}

	return "", false
}

// IndexedModel pairs a ModelOption with its 1-based global index.
type IndexedModel struct {
	ModelOption
	GlobalIdx int
}

// ParseModelArgs parses /model arguments as a query string.
// Returns empty string when no arguments are provided.
func ParseModelArgs(args string) string {
	return strings.TrimSpace(args)
}

// IndexModels wraps a full model list with sequential 1-based indices.
func IndexModels(models []ModelOption) []IndexedModel {
	out := make([]IndexedModel, len(models))
	for i, m := range models {
		out[i] = IndexedModel{ModelOption: m, GlobalIdx: i + 1}
	}
	return out
}

// FilterModels returns indexed models matching the query, preserving
// their 1-based global indices from the full list.
func FilterModels(models []ModelOption, query string) []IndexedModel {
	query = strings.ToLower(query)
	var out []IndexedModel
	for i, m := range models {
		label := strings.ToLower(m.Provider + "/" + m.Model)
		if strings.Contains(label, query) {
			out = append(out, IndexedModel{ModelOption: m, GlobalIdx: i + 1})
		}
	}
	return out
}
