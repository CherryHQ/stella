package runtime

import (
	"fmt"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

const currentSpeakerInstruction = "The human speaking in this group turn. Use this only for addressing and tone. Private profile facts are not injected into public group prompts. Do not call `memory_read` with ref `profile` or disclose profile details in a group unless this speaker explicitly asks you to read or use their profile in this conversation."

func withCurrentSpeakerContext(msg MessageContent, speaker memory.CurrentSpeaker) MessageContent {
	if speaker == (memory.CurrentSpeaker{}) {
		return msg
	}

	return prefixMessage(msg, currentSpeakerContextText(speaker))
}

// withGroupWakeContext prefixes the model's copy of the trigger with why this
// turn is running. Without it an agent cannot tell "you were asked" from "a
// peer said something near you", and answers both the same way.
func withGroupWakeContext(msg MessageContent, wake memory.GroupWake) MessageContent {
	if wake.Reason == "" {
		return msg
	}
	return prefixMessage(msg, groupWakeContextText(wake))
}

func groupWakeContextText(wake memory.GroupWake) string {
	held := ""
	if wake.HeldUpToSeq > 0 {
		held = fmt.Sprintf("\nYou already drafted a reply to this and it was held: peers posted up to seq %d while you were writing. Read what changed and answer the current state, not your draft.", wake.HeldUpToSeq)
	}
	return fmt.Sprintf("<wake>\nWhy you are running this turn: %s.%s\n</wake>", wake.Reason, held)
}

func prefixMessage(msg MessageContent, prefix string) MessageContent {
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

func currentSpeakerContextText(speaker memory.CurrentSpeaker) string {
	name := speaker.DisplayName
	if name == "" {
		name = "Unknown"
	}
	linked := "no"
	if speaker.UserID != "" {
		linked = "yes (profile available only by explicit request)"
	}
	return fmt.Sprintf("<current_speaker>\n%s\n\nName: %s\nLinked Stella user: %s\n</current_speaker>", currentSpeakerInstruction, name, linked)
}
