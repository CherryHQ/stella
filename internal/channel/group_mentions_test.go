package channel

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// The two text entry points share one scanner and must keep disagreeing exactly
// where they disagreed before: agent prose is forgiving (punctuation, display
// names, one wake per peer), the web composer is literal.
func TestParseWebMentionsStaysLiteral(t *testing.T) {
	members := []sqlc.ChannelGroupMember{{AgentID: "agent-1"}, {AgentID: "agent-2"}}

	if got := parseWebMentions("nobody here", members); got != nil {
		t.Fatalf("mentions = %#v, want nil for a message with no @", got)
	}
	if got := parseWebMentions("hi @agent-1 and @agent-3", members); !reflect.DeepEqual(got, []pkgchannel.Mention{
		{Raw: "@agent-1", AgentID: "agent-1"},
	}) {
		t.Fatalf("mentions = %#v, want only the member token", got)
	}
	// No punctuation trimming: the composer inserts a bare token, so a token
	// with punctuation clinging to it was not what the UI showed the user.
	if got := parseWebMentions("hi @agent-1, bye", members); got != nil {
		t.Fatalf("mentions = %#v, want no match for a token the composer did not insert", got)
	}
	// No dedup: web ingest has always emitted one mention per written @.
	if got := parseWebMentions("@agent-1 @agent-1", members); len(got) != 2 {
		t.Fatalf("mentions = %#v, want one per written @", got)
	}
}

func TestParseGroupMentionsMatchesIDsNamesAndDedups(t *testing.T) {
	db := dbtest.New(t)
	q := sqlc.New(db)
	ctx := context.Background()
	if _, err := q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: "agent-1", Name: "Ada", Workspace: t.TempDir(),
		Sandbox: json.RawMessage("{}"), Scope: "system", Enabled: true,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	members := []sqlc.ChannelGroupMember{{AgentID: "agent-1"}}

	if got := parseGroupMentions(ctx, q, "nothing here", members); !reflect.DeepEqual(got, []pkgchannel.Mention{}) {
		t.Fatalf("mentions = %#v, want a non-nil empty list", got)
	}
	// Prose: the trailing comma belongs to the sentence, the display name is how
	// an agent knows its peers, and naming a peer twice wakes it once.
	got := parseGroupMentions(ctx, q, "(@Ada), then @agent-1 again", members)
	if !reflect.DeepEqual(got, []pkgchannel.Mention{{Raw: "@Ada", AgentID: "agent-1"}}) {
		t.Fatalf("mentions = %#v, want one deduplicated mention resolved by display name", got)
	}
}
