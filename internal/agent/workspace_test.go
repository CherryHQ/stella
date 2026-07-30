package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentDirInHome(t *testing.T) {
	base := filepath.Join("/srv", "stella")
	cases := []struct {
		name string
		home string
		got  string
		want string
	}{
		{
			name: "user",
			home: UserHomeDir(base, "u1"),
			got:  UserAgentDir(base, "u1", "a1"),
			want: filepath.Join(base, "users", "u1", "agents", "a1"),
		},
		{
			name: "group",
			home: GroupHomeDir(base, "g1"),
			got:  GroupAgentDir(base, "g1", "a1"),
			want: filepath.Join(base, "users", "group-g1", "agents", "a1"),
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := AgentDirInHome(tt.home, "a1"); got != tt.want {
				t.Errorf("AgentDirInHome = %q, want %q", got, tt.want)
			}
			if tt.got != tt.want {
				t.Errorf("specialized helper = %q, want %q", tt.got, tt.want)
			}
		})
	}
}

// Uploads and user skills must live under the shared user-data root (data/,
// mounted as /user), not at the user-home root — that is what makes them
// reachable in-sandbox at $STELLA_USER_DIR and shared across the user's agents.
func TestUserDataDirHoldsAssetsAndSkills(t *testing.T) {
	home := filepath.Join("/srv", "users", "u1")
	data := UserDataDir(home)
	if want := filepath.Join(home, "data"); data != want {
		t.Fatalf("UserDataDir = %q, want %q", data, want)
	}
	if got, want := UserAssetsDir(home), filepath.Join(data, "assets"); got != want {
		t.Errorf("UserAssetsDir = %q, want %q", got, want)
	}
	if got, want := UserSkillsDir(data), filepath.Join(data, ".agents", "skills"); got != want {
		t.Errorf("UserSkillsDir(data) = %q, want %q", got, want)
	}
}

func TestSetupAgentWorkspace(t *testing.T) {
	base := t.TempDir()

	dir, err := SetupAgentWorkspace(base, "stella")
	if err != nil {
		t.Fatalf("SetupAgentWorkspace: %v", err)
	}

	want := filepath.Join(base, "agents", "stella")
	if dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}

	// Verify the skills subdirectory was created.
	skillsDir := filepath.Join(dir, ".agents", "skills")
	info, err := os.Stat(skillsDir)
	if err != nil {
		t.Fatalf("skills dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("skills path is not a directory")
	}
}

func TestSetupAgentWorkspaceIdempotent(t *testing.T) {
	base := t.TempDir()

	dir1, err := SetupAgentWorkspace(base, "stella")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	dir2, err := SetupAgentWorkspace(base, "stella")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if dir1 != dir2 {
		t.Errorf("dirs differ: %q vs %q", dir1, dir2)
	}
}

func TestSetupAgentWorkspaceEmptyID(t *testing.T) {
	_, err := SetupAgentWorkspace(t.TempDir(), "")
	if err == nil {
		t.Error("expected error for empty agent ID")
	}
}

func TestSetupUserWorkspace(t *testing.T) {
	base := t.TempDir()
	userDir, err := SetupUserWorkspace(base, "42", "agent-1")
	if err != nil {
		t.Fatalf("SetupUserWorkspace: %v", err)
	}

	want := filepath.Join(base, "users", "42")
	if userDir != want {
		t.Errorf("dir = %q, want %q", userDir, want)
	}

	// Verify the shared user-data subtree (mounted as /user) and the per-agent
	// private area.
	for _, sub := range []string{
		filepath.Join(userDir, "data", ".agents", "skills"),
		filepath.Join(userDir, "data", ".agents", "delegates"),
		filepath.Join(userDir, "data", "assets"),
		filepath.Join(userDir, "agents", "agent-1"),
	} {
		info, err := os.Stat(sub)
		if err != nil {
			t.Errorf("dir %q not created: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", sub)
		}
	}
}

func TestSetupUserWorkspaceEmptyAgent(t *testing.T) {
	_, err := SetupUserWorkspace(t.TempDir(), "1", "")
	if err == nil {
		t.Error("expected error for empty agent ID")
	}
}

func TestSetupUserWorkspaceInvalidUser(t *testing.T) {
	_, err := SetupUserWorkspace(t.TempDir(), "", "agent-1")
	if err == nil {
		t.Error("expected error for empty user ID")
	}
}

func TestSetupUserWorkspaceIdempotent(t *testing.T) {
	base := t.TempDir()
	d1, err := SetupUserWorkspace(base, "42", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	// Create a file to verify it survives second setup.
	testFile := filepath.Join(d1, "data", "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	d2, err := SetupUserWorkspace(base, "42", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Errorf("paths differ: %q vs %q", d1, d2)
	}
	if _, err := os.Stat(testFile); err != nil {
		t.Error("file disappeared after second setup")
	}
}

// TestSetupUserWorkspaceSharedAcrossAgents verifies the user home is the same
// directory regardless of which agent set it up — toolchains, skills, and uploads
// are shared across a user's agents (#442).
func TestSetupUserWorkspaceSharedAcrossAgents(t *testing.T) {
	base := t.TempDir()
	d1, err := SetupUserWorkspace(base, "42", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := SetupUserWorkspace(base, "42", "agent-2")
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Errorf("same user under different agents should share one home: %q vs %q", d1, d2)
	}
	// Each agent still gets its own private subdir for projects.
	if a1, a2 := UserAgentDir(base, "42", "agent-1"), UserAgentDir(base, "42", "agent-2"); a1 == a2 {
		t.Error("different agents should have distinct private areas under the user home")
	}
}

func TestSetupUserWorkspaceIsolation(t *testing.T) {
	base := t.TempDir()
	d1, err := SetupUserWorkspace(base, "1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := SetupUserWorkspace(base, "2", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d2 {
		t.Error("different users should have different paths")
	}

	// Write to user 1 data, verify user 2 doesn't see it.
	if err := os.WriteFile(filepath.Join(d1, "data", "secret.txt"), []byte("u1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d2, "data", "secret.txt")); !os.IsNotExist(err) {
		t.Error("user 2 should not see user 1's file")
	}
}

func TestUserSkillsDir(t *testing.T) {
	got := UserSkillsDir("/base/users/42")
	want := filepath.Join("/base/users/42", ".agents", "skills")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
