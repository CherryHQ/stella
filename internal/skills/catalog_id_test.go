package skills

import (
	"strings"
	"testing"
)

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
		if strings.ContainsAny(id, "/?#%") || strings.Trim(id, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			t.Fatalf("encoded ID %q is not a URL-safe path segment", id)
		}
		scope, user, agent, name, err := decodeFilesystemSkillID(id)
		if err != nil || scope != tc.scope || user != tc.user || agent != tc.agent || name != tc.name {
			t.Fatalf("round trip %q = %q %q %q %q, %v", id, scope, user, agent, name, err)
		}
	}
	for _, id := range []string{"", "skill/v1/6:system0:0:4:base", "skill-v2-c3lzdGVt", "skill-v2-c3lzdGVt=", "skill-v2-c3lzdGVtAA", "skill-v2-" + strings.Repeat("A", maxFilesystemSkillIDSize)} {
		if _, _, _, _, err := decodeFilesystemSkillID(id); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	id, err := encodeFilesystemSkillID("user", "u", "", "base")
	if err != nil {
		t.Fatal(err)
	}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, id[len(id)-1])
	nonCanonical := id[:len(id)-1] + string(alphabet[(last&^3)|((last+1)&3)])
	for _, malformed := range []string{"skill-v2-***", id + "A", nonCanonical} {
		if _, _, _, _, err := decodeFilesystemSkillID(malformed); err == nil {
			t.Fatalf("accepted malformed/noncanonical ID %q", malformed)
		}
	}
	if _, err := encodeFilesystemSkillID("user", strings.Repeat("u", maxFilesystemSkillIDSize), "", "base"); err == nil {
		t.Fatal("accepted an unbounded filesystem Skill ID")
	}
	for _, tc := range []struct{ scope, user, agent string }{{"user", "../u", ""}, {"user", "u/x", ""}, {"system_agent", "", ".."}, {"user_agent", "u", "a/b"}} {
		if _, err := encodeFilesystemSkillID(tc.scope, tc.user, tc.agent, "base"); err == nil {
			t.Fatalf("accepted unsafe owner %+v", tc)
		}
	}
}
