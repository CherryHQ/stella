package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/skills"
)

// The on-disk layout is user-first (#442): a principal — a user, or a channel
// group treated as a synthetic user — has one home shared by all of its agents,
// the "real PC" model where the principal has a home and an agent is an app run
// under it. Toolchains, skills, caches, and uploads live at the principal level
// and are shared across its agents; a project's working tree stays scoped to the
// agent that owns it, under that agent's subdir of the home.
//
//	{base}/agents/{agentID}/                  agent definition + agent-level skills (user-independent)
//	{base}/users/{userID}/                    THE user home (sandbox $HOME)
//	  .mise-tools/                            per-user toolchain, shared by all agents (#424)
//	  .agents/skills/                         user-level skills, shared
//	  data/  assets/                          user data + uploads, shared
//	  agents/{agentID}/projects/{projectID}/  project working tree = sandbox cwd, owned by the agent
//	{base}/users/group-{groupID}/             a channel group's home — same shape, a shared "account"
//
// The users tree is the only top-level isolation boundary. A channel group is its
// own principal (one home for the whole group), keyed by the group ID under a
// "group-" prefix so a group home can never collide with a user home of the same
// raw ID. User-less agent jobs (e.g. builtin scheduled jobs) have no principal
// home and run in the agent's own workspace, {base}/agents/{agentID}/.

// AgentWorkspaceDir returns the user-independent agent directory that holds the
// agent definition and agent-level skills.
func AgentWorkspaceDir(base, agentID string) string {
	return filepath.Join(base, "agents", agentID)
}

// UserHomeDir returns the home directory for a user, shared by all the user's agents.
func UserHomeDir(base, userID string) string {
	return filepath.Join(base, "users", userID)
}

// GroupHomeDir returns the home directory for a channel group — a principal in
// the users tree, shared by all the group's agents. The "group-" prefix keeps a
// group home from colliding with a user home of the same raw ID.
func GroupHomeDir(base, groupID string) string {
	return filepath.Join(base, "users", "group-"+groupID)
}

// UserAgentDir returns the per-(user, agent) private area under a user home.
// Projects owned by the agent live under this directory.
func UserAgentDir(base, userID, agentID string) string {
	return filepath.Join(UserHomeDir(base, userID), "agents", agentID)
}

// GroupAgentDir returns the per-(group, agent) private area under a group home.
func GroupAgentDir(base, groupID, agentID string) string {
	return filepath.Join(GroupHomeDir(base, groupID), "agents", agentID)
}

// SetupAgentWorkspace ensures the user-independent agent directory exists.
// Creates {base}/agents/{agentID}/.agents/skills/ and returns the agent directory.
func SetupAgentWorkspace(base, agentID string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be empty")
	}
	dir := AgentWorkspaceDir(base, agentID)
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "skills"), 0o755); err != nil {
		return "", fmt.Errorf("create agent workspace for agent %q: %w", agentID, err)
	}
	return dir, nil
}

// SetupUserWorkspace ensures a user home and the per-(user, agent) area exist.
// Creates the user home (.agents/skills, data, assets — shared by all the user's
// agents) and the agent's private subdir (where its projects live). Returns the
// user home directory, which is the sandbox workspace root and HOME.
func SetupUserWorkspace(base, userID, agentID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("user ID must not be empty")
	}
	return setupHome(UserHomeDir(base, userID), UserAgentDir(base, userID, agentID), agentID)
}

// SetupGroupWorkspace mirrors SetupUserWorkspace for a group account.
func SetupGroupWorkspace(base, groupID, agentID string) (string, error) {
	if groupID == "" {
		return "", fmt.Errorf("group ID must not be empty")
	}
	return setupHome(GroupHomeDir(base, groupID), GroupAgentDir(base, groupID, agentID), agentID)
}

// setupHome creates the shared home directories and, when agentDir is non-empty,
// the agent's private subdir under the home.
func setupHome(home, agentDir, agentID string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be empty")
	}
	// data/ is the shared user-data root mounted as /user. Its subtree holds the
	// per-user cache, user-level skills/delegates, and uploads — all shared across
	// the user's agents. The per-user mise tree is a sibling of data/ (under the
	// STELLA_HOME frame, created on demand by EnsureUserMiseHome), not here.
	for _, sub := range [][]string{
		{"data"},
		{"data", ".cache"},
		{"data", ".agents", "skills"},
		{"data", ".agents", "delegates"},
		{"data", "assets"},
	} {
		if err := os.MkdirAll(filepath.Join(home, filepath.Join(sub...)), 0o755); err != nil {
			return "", fmt.Errorf("create home dir %q: %w", home, err)
		}
	}
	if agentDir != "" {
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			return "", fmt.Errorf("create per-agent area %q: %w", agentDir, err)
		}
	}
	return home, nil
}

// UserDataDir returns the shared user-data root within a user home. Toolchains,
// caches, user-level skills/delegates, and uploaded assets live here, shared by
// all the user's agents; it is mounted as /user in the two-root sandbox layout.
// Takes the resolved home so it composes with both user and group homes.
func UserDataDir(userHome string) string {
	return filepath.Join(userHome, "data")
}

// UserAssetsDir returns the per-user assets directory within a user home.
// Uploaded files from all channels are stored here, shared across the user's
// agents, under the user-data root mounted as /user (reachable in-sandbox at
// $STELLA_USER_DIR/assets).
func UserAssetsDir(userHome string) string {
	return filepath.Join(UserDataDir(userHome), "assets")
}

// UserSkillsDir returns the user-level skills directory within a user home.
func UserSkillsDir(userHome string) string {
	return filepath.Join(userHome, ".agents", "skills")
}

// SystemDBSkillsDir returns the directory holding DB-installed system-scope
// skills, a sibling of the shipped built-in skills dir (UserSkillsDir(base),
// read by ListSystemSkills). Keeping runtime caches apart prevents DB Skills
// from being mistaken for shipped built-ins. Isolating backends mount it
// read-only.
func SystemDBSkillsDir(base string) string {
	return filepath.Join(base, ".agents", "db-skills")
}

// skillDiskLayout assembles the per-scope skill layout under the given roots.
// An empty root zeroes its scope so SkillDiskLayout.BaseDir returns "" there.
func skillDiskLayout(systemDB, agentRoot, userDataDir, userAgentRoot string) skills.SkillDiskLayout {
	l := skills.SkillDiskLayout{SystemDB: systemDB}
	if agentRoot != "" {
		l.Agent = UserSkillsDir(agentRoot)
	}
	if userDataDir != "" {
		l.User = UserSkillsDir(userDataDir)
	}
	if userAgentRoot != "" {
		l.UserAgent = UserSkillsDir(userAgentRoot)
	}
	return l
}
