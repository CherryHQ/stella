package runtime

import (
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	currentSpeakerInstruction = "The human speaking in this group turn. Use this only for addressing and tone. Private profile facts are not injected into public group prompts. Do not call `memory.read` with ref `profile` or disclose profile details in a group unless this speaker explicitly asks you to read or use their profile in this conversation."
	publicActorInstruction    = "The human speaking in this group turn. Use this public display name only for addressing and tone. Private Profile, Soul, Constraint, and one-to-one Knowledge are unavailable in structured group chat."
)

func withCurrentSpeakerContext(msg MessageContent, speaker memory.CurrentSpeaker, privateProfileAvailable bool) MessageContent {
	if speaker == (memory.CurrentSpeaker{}) {
		return msg
	}

	prefix := currentSpeakerContextText(speaker, privateProfileAvailable)
	switch m := msg.(type) {
	case string:
		return prefix + "\n\n" + m
	case []ai.ContentBlock:
		out := make([]ai.ContentBlock, 0, len(m)+1)
		out = append(out, ai.TextContent{Text: prefix})
		out = append(out, m...)
		return out
	default:
		return prefix + "\n\n" + fmt.Sprintf("%v", m)
	}
}

func currentSpeakerContextText(speaker memory.CurrentSpeaker, privateProfileAvailable bool) string {
	name := speaker.DisplayName
	if name == "" {
		name = "Unknown"
	}
	if !privateProfileAvailable {
		return fmt.Sprintf("<current_speaker>\n%s\n\nName: %s\n</current_speaker>", publicActorInstruction, name)
	}
	linked := "no"
	if speaker.UserID != "" {
		linked = "yes (profile available only by explicit request)"
	}
	return fmt.Sprintf("<current_speaker>\n%s\n\nName: %s\nLinked Stella user: %s\n</current_speaker>", currentSpeakerInstruction, name, linked)
}
