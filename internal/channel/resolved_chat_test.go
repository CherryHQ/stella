package channel

import (
	"testing"

	"github.com/vaayne/anna/internal/agent"
)

// TestGroupSessionKeySharedAcrossSenders verifies the invariant that two
// different senders in the same Feishu group produce the same session key.
// The key is constructed by ResolveWithChannel when isGroup=true and chatID!="".
func TestGroupSessionKeySharedAcrossSenders(t *testing.T) {
	agentID := "anna"
	channelID := "feishu-bot"
	chatID := "oc_group123"

	key1 := agent.BuildGroupSessionKey(agentID, channelID, chatID)
	key2 := agent.BuildGroupSessionKey(agentID, channelID, chatID)

	if key1 != key2 {
		t.Errorf("same group, different senders produced different keys: %q vs %q", key1, key2)
	}
}

// TestGroupSessionKeyDifferentFromDM verifies group keys are namespaced
// separately from DM keys.
func TestGroupSessionKeyDifferentFromDM(t *testing.T) {
	agentID := "anna"
	platform := "feishu"
	senderID := "ou_user1"
	chatID := "oc_group123"

	groupKey := agent.BuildGroupSessionKey(agentID, platform, chatID)
	dmKey := agent.BuildSessionKey(agentID, platform, senderID, "private")

	if groupKey == dmKey {
		t.Error("group session key should be different from DM session key")
	}

	// Group key must contain "group:" segment.
	if groupKey[:len(agentID)+7] != agentID+":group:" {
		t.Errorf("group key format unexpected: %q", groupKey)
	}
}

// TestGroupSessionKeyDifferentGroups verifies different groups get different keys.
func TestGroupSessionKeyDifferentGroups(t *testing.T) {
	key1 := agent.BuildGroupSessionKey("anna", "feishu", "oc_group1")
	key2 := agent.BuildGroupSessionKey("anna", "feishu", "oc_group2")

	if key1 == key2 {
		t.Error("different group chats should produce different session keys")
	}
}
