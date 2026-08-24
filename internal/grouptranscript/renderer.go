package grouptranscript

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// GroupTranscriptEvent is the model-facing projection of one public group
// event. Rendering is deliberately centralized because every surface that puts
// group text into a prompt must preserve its one-event, one-physical-line
// boundary.
type GroupTranscriptEvent struct {
	Seq           int64
	ActorType     string
	DisplayName   string
	Content       string
	You           bool
	DeliveryState string
}

// RenderGroupTranscriptLine produces one physical transcript line. Content is
// normalized before JSON-string escaping, so an event cannot create another
// transcript row through a newline or an invisible formatting code point.
func RenderGroupTranscriptLine(event GroupTranscriptEvent) string {
	name := handleDisplayName(SanitizeGroupParticipantName(event.DisplayName), event.ActorType)
	if event.You {
		name += " (you)"
	}
	content := EscapeGroupTranscriptContent(event.Content)
	if event.You {
		switch event.DeliveryState {
		case "pending":
			content += " (sending)"
		case "failed":
			content += " (delivery failed — peers never saw this)"
		}
	}
	return "[seq:" + strconv.FormatInt(event.Seq, 10) + " " + name + "]: " + content
}

// RenderGroupSystemLine renders trusted assembler metadata through the same
// content normalization as member events. This keeps the omitted-history marker
// structurally impossible for a member message to forge.
func RenderGroupSystemLine(content string) string {
	return "[system]: " + EscapeGroupTranscriptContent(content)
}

// RenderGroupToolActivityNote is a private prompt annotation for a stopped
// turn. Tool names are escaped as content, never replayed as tool calls or
// results.
func RenderGroupToolActivityNote(seq int64, toolNames []string) string {
	return "[note: you ran tools (" + EscapeGroupTranscriptContent(strings.Join(toolNames, ", ")) + ") at seq " + strconv.FormatInt(seq, 10) + " without replying]"
}

func handleDisplayName(name, actorType string) string {
	if actorType == "agent" && !strings.HasPrefix(name, "@") {
		return "@" + name
	}
	return name
}

// EscapeGroupTranscriptContent returns the normalized content value in JSON
// string escaping semantics, without its surrounding quotes.
func EscapeGroupTranscriptContent(content string) string {
	quoted := strconv.Quote(normalizeGroupTranscriptText(content))
	return quoted[1 : len(quoted)-1]
}

// SanitizeGroupParticipantName makes an attacker-controlled display name safe
// for the transcript label while keeping ordinary Unicode names readable.
func SanitizeGroupParticipantName(name string) string {
	name = normalizeGroupTranscriptText(name)
	name = strings.NewReplacer("[", "", "]", "").Replace(name)
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "unknown"
	}
	runes := []rune(name)
	if len(runes) > 64 {
		return string(runes[:64])
	}
	return name
}

func normalizeGroupTranscriptText(text string) string {
	text = norm.NFKC.String(text)
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u2028", "\n", "\u2029", "\n").Replace(text)
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		if unicode.Is(unicode.Cf, r) || isDefaultIgnorable(r) {
			continue
		}
		if r < 0x20 && r != '\n' && r != '\t' {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// isDefaultIgnorable covers the Default_Ignorable_Code_Point ranges outside
// Unicode category Cf. Cf itself is checked by the caller for future Unicode
// additions that belong to that category.
func isDefaultIgnorable(r rune) bool {
	switch {
	case r == 0x00AD || r == 0x034F || r == 0x061C || r == 0x115F || r == 0x1160:
		return true
	case r >= 0x17B4 && r <= 0x17B5:
		return true
	case r >= 0x180B && r <= 0x180F:
		return true
	case r >= 0x200B && r <= 0x200F:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2060 && r <= 0x206F:
		return true
	case r == 0x3164 || r == 0xFE00 || (r >= 0xFE01 && r <= 0xFE0F) || r == 0xFEFF || r == 0xFFA0:
		return true
	case r >= 0xFFF0 && r <= 0xFFF8:
		return true
	case r >= 0x1BCA0 && r <= 0x1BCA3:
		return true
	case r >= 0x1D173 && r <= 0x1D17A:
		return true
	case r == 0xE0000 || r == 0xE0001 || (r >= 0xE0002 && r <= 0xE001F) || (r >= 0xE0020 && r <= 0xE007F):
		return true
	case r >= 0xE0100 && r <= 0xE01EF:
		return true
	default:
		return false
	}
}
