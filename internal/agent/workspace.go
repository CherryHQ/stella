package agent

import "path/filepath"

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

// AgentDirInHome returns the per-agent private area under a principal home.
// Projects owned by the agent live under this directory.
func AgentDirInHome(home, agentID string) string {
	return filepath.Join(home, "agents", agentID)
}

// UserAgentDir returns the per-(user, agent) private area under a user home.
func UserAgentDir(base, userID, agentID string) string {
	return AgentDirInHome(UserHomeDir(base, userID), agentID)
}

// GroupAgentDir returns the per-(group, agent) private area under a group home.
func GroupAgentDir(base, groupID, agentID string) string {
	return AgentDirInHome(GroupHomeDir(base, groupID), agentID)
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
// agents, under the user-data root mounted internally at /user (Agent-facing
// access uses $STELLA_ASSETS_DIR).
func UserAssetsDir(userHome string) string {
	return filepath.Join(UserDataDir(userHome), "assets")
}
