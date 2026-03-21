package agent

import "testing"

func TestBuildSessionKey(t *testing.T) {
	tests := []struct {
		name           string
		agentID        string
		platform       string
		externalUserID string
		channelContext string
		want           string
	}{
		{
			name:           "private DM",
			agentID:        "anna",
			platform:       "tg",
			externalUserID: "123456",
			channelContext: "private",
			want:           "anna:tg:123456:private",
		},
		{
			name:           "group chat",
			agentID:        "anna",
			platform:       "tg",
			externalUserID: "123456",
			channelContext: "group:-987654",
			want:           "anna:tg:123456:group:-987654",
		},
		{
			name:           "different agent",
			agentID:        "coder",
			platform:       "tg",
			externalUserID: "123456",
			channelContext: "private",
			want:           "coder:tg:123456:private",
		},
		{
			name:           "CLI platform",
			agentID:        "anna",
			platform:       "cli",
			externalUserID: "local",
			channelContext: "default",
			want:           "anna:cli:local:default",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildSessionKey(tt.agentID, tt.platform, tt.externalUserID, tt.channelContext)
			if got != tt.want {
				t.Errorf("BuildSessionKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildSessionKeyDifferentAgentsDifferentKeys(t *testing.T) {
	key1 := BuildSessionKey("anna", "tg", "123", "private")
	key2 := BuildSessionKey("coder", "tg", "123", "private")
	if key1 == key2 {
		t.Error("different agents should produce different session keys")
	}
}

func TestBuildUserSessionKey(t *testing.T) {
	tests := []struct {
		name           string
		agentID        string
		authUserID     int64
		channelContext string
		want           string
	}{
		{
			name:           "linked user private",
			agentID:        "anna",
			authUserID:     42,
			channelContext: "private",
			want:           "anna:user:42:private",
		},
		{
			name:           "different agent",
			agentID:        "coder",
			authUserID:     42,
			channelContext: "private",
			want:           "coder:user:42:private",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildUserSessionKey(tt.agentID, tt.authUserID, tt.channelContext)
			if got != tt.want {
				t.Errorf("BuildUserSessionKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLinkedUserSessionKeySharedAcrossChannels(t *testing.T) {
	// Same auth user on different platforms should get the same session key.
	keyTG := BuildUserSessionKey("anna", 42, "private")
	keyQQ := BuildUserSessionKey("anna", 42, "private")
	if keyTG != keyQQ {
		t.Errorf("linked user keys should match across platforms: %q != %q", keyTG, keyQQ)
	}

	// But different agents should differ.
	keyOther := BuildUserSessionKey("coder", 42, "private")
	if keyTG == keyOther {
		t.Error("different agents should produce different keys for same user")
	}
}

func TestCompactionConfigWithDefaults(t *testing.T) {
	tests := []struct {
		name          string
		input         CompactionConfig
		wantMaxTokens int
		wantKeepTail  int
	}{
		{"zero values get defaults", CompactionConfig{}, 80_000, 20},
		{"custom values preserved", CompactionConfig{MaxTokens: 50_000, KeepTail: 10}, 50_000, 10},
		{"negative MaxTokens preserved", CompactionConfig{MaxTokens: -1}, -1, 20},
		{"partial override", CompactionConfig{KeepTail: 5}, 80_000, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.WithDefaults()
			if got.MaxTokens != tt.wantMaxTokens {
				t.Errorf("MaxTokens = %d, want %d", got.MaxTokens, tt.wantMaxTokens)
			}
			if got.KeepTail != tt.wantKeepTail {
				t.Errorf("KeepTail = %d, want %d", got.KeepTail, tt.wantKeepTail)
			}
		})
	}
}
