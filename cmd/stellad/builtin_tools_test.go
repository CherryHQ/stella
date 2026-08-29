package main

import (
	"context"
	"slices"
	"testing"

	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/internal/vault"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type stubNotifier struct{}

func (stubNotifier) Notify(context.Context, pkgchannel.Notification) error { return nil }

func (stubNotifier) NotifyUser(context.Context, string, pkgchannel.Notification) error { return nil }

// defaultModelFacingTools is a golden record of the tool names a default
// deployment puts in front of the model: the sandbox core tools, the skills
// tool, and every builtin. It exists for the action-split work (#1173), where
// one tool name becomes several: this list is the one place a rename, addition,
// or removal has to show up as a reviewable diff instead of being discovered in
// production. Update it deliberately, together with the skills, prompts, and
// tool_override rows that carry the same names — never just to make the test
// pass.
//
// Deliberately not covered, because they vary per deployment rather than being
// part of the default surface: plugin tools, MCP tools, and per-run tools.
var defaultModelFacingTools = []string{
	"bash",
	"view_image",
	skillstool.ToolName,
	"memory",
	"notify",
	"goal_cancel",
	"goal_create",
	"goal_get",
	"goal_list",
	"session",
	"library_search",
	"scheduler_job_create",
	"scheduler_job_delete",
	"scheduler_job_get",
	"scheduler_job_list",
	"scheduler_job_pause",
	"scheduler_job_resume",
	"scheduler_job_update",
	"workflow_get",
	"workflow_list",
	"workflow_run",
	"workflow_save",
	"oauth_connect",
	"oauth_disconnect",
	"oauth_flow_status",
	"oauth_list",
	"email_account_list",
	"email_message_list",
	"email_message_read",
	"email_message_send",
	"share_create_article",
	"share_create_artifact",
	"share_list",
	"share_revoke",
	"recally_article_get",
	"recally_article_list",
	"recally_article_save",
	"recally_digest_get",
	"recally_digest_save",
	"recally_entry_add",
	"recally_entry_list",
	"recally_entry_update",
	"recally_feed_add",
	"recally_feed_list",
	"recally_feed_poll",
	"recally_feed_remove",
	"vault_secret_delete",
	"vault_secret_list",
	"vault_secret_set",
}

// defaultToolNames is the same surface the runner assembles, minus the pieces
// that need a live deployment. Services stay nil: a tool's Definition never
// reaches its service. The notifier and the vault are the two exceptions,
// because a nil one removes its tool from the set entirely.
func defaultToolNames(t *testing.T) []string {
	t.Helper()
	names := []string{skillstool.ToolName}
	for _, core := range agentsandbox.ToolDefinitionsWithAvailability() {
		names = append(names, core.Definition.Name)
	}
	for _, builtin := range newBuiltinTools(builtinToolDeps{Notifier: stubNotifier{}, Vault: &vault.Service{}}) {
		definition, ok := builtin.Definition()
		if !ok || definition.Name == "" {
			t.Fatalf("builtin tool has no usable definition: %#v", definition)
		}
		names = append(names, definition.Name)
	}
	slices.Sort(names)
	return names
}

func TestDefaultToolNamesMatchGolden(t *testing.T) {
	got := defaultToolNames(t)
	want := slices.Clone(defaultModelFacingTools)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the model-facing tool names changed.\n got: %q\nwant: %q\nIf this is intended, update defaultModelFacingTools along with the skills, prompts, and docs naming these tools.", got, want)
	}
}
