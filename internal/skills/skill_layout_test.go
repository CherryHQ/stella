package skills

import "testing"

func TestSkillDiskLayout_BaseDirByScope(t *testing.T) {
	l := SkillDiskLayout{
		SystemDB:  "/base/.agents/db-skills",
		Agent:     "/base/agents/a1/.agents/skills",
		User:      "/base/users/u1/data/.agents/skills",
		UserAgent: "/base/users/u1/agents/a1/.agents/skills",
	}
	cases := map[string]string{
		"system":       l.SystemDB,
		"system_agent": l.Agent,
		"user":         l.User,
		"user_agent":   l.UserAgent,
		"project":      "", // not a disk-managed scope
		"":             "",
	}
	for scope, want := range cases {
		if got := l.BaseDir(scope); got != want {
			t.Errorf("BaseDir(%q) = %q, want %q", scope, got, want)
		}
	}
}

func TestSkillDiskLayout_Dir(t *testing.T) {
	l := SkillDiskLayout{User: "/base/users/u1/data/.agents/skills"}

	if got, want := l.Dir("user", "skill-123"), "/base/users/u1/data/.agents/skills/skill-123"; got != want {
		t.Errorf("Dir(user, skill-123) = %q, want %q", got, want)
	}
	// Unmapped scope and empty identity both yield "" — no false path is emitted.
	if got := l.Dir("system", "skill-123"); got != "" {
		t.Errorf("Dir(system, skill-123) = %q, want \"\" (zero-value scope)", got)
	}
	if got := l.Dir("user", ""); got != "" {
		t.Errorf("Dir(user, \"\") = %q, want \"\"", got)
	}
}
