package skills

import "testing"

func TestFilesystemSkillIDRoundTripAndRejectsNonCanonicalInput(t *testing.T) {
	for _, tc := range []struct{ scope, user, agent, name string }{
		{"system", "", "", "base"},
		{"system_agent", "", "agent-1", "base"},
		{"user", "4b848a91-4f65-462e-b2c1-6d9f48c7e75f", "", "base"},
		{"user_agent", "4b848a91-4f65-462e-b2c1-6d9f48c7e75f", "agent-1", "base"},
	} {
		id, err := encodeFilesystemSkillID(tc.scope, tc.user, tc.agent, tc.name)
		if err != nil {
			t.Fatalf("encode %+v: %v", tc, err)
		}
		scope, user, agent, name, err := decodeFilesystemSkillID(id)
		if err != nil || scope != tc.scope || user != tc.user || agent != tc.agent || name != tc.name {
			t.Fatalf("round trip %q = %q %q %q %q, %v", id, scope, user, agent, name, err)
		}
	}
	for _, id := range []string{"", "skill/v1/06:system0:0:4:base", "skill/v1/6:system0:0:4:base/", "skill/v1/999999999999999999999999999999999999999999999999999999999999:system0:0:4:base"} {
		if _, _, _, _, err := decodeFilesystemSkillID(id); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	for _, tc := range []struct{ scope, user, agent string }{{"user", "../u", ""}, {"user", "u/x", ""}, {"system_agent", "", ".."}, {"user_agent", "u", "a/b"}} {
		if _, err := encodeFilesystemSkillID(tc.scope, tc.user, tc.agent, "base"); err == nil {
			t.Fatalf("accepted unsafe owner %+v", tc)
		}
	}
}
