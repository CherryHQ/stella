package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	groupClaimToolName   = "group_claim"
	groupReleaseToolName = "group_release"
	groupClaimsToolName  = "group_claims"
	defaultGroupClaimTTL = 10 * time.Minute
	minimumGroupClaimTTL = time.Minute
	maximumGroupClaimTTL = 24 * time.Hour
)

// GroupClaimTools exposes ephemeral, group-scoped work ownership. It is
// registered only for group runs, so no direct-message caller can create a
// cross-agent coordination record.
type GroupClaimTools struct{ q *sqlc.Queries }

func NewGroupClaimTools(db *pgxpool.Pool) *GroupClaimTools {
	if db == nil {
		return nil
	}
	return &GroupClaimTools{q: sqlc.New(db)}
}

// NewGroupClaimPromptLoader returns the read-only prompt projection for live
// peer claims. It lives with the claim store so the composition root does not
// reach through to SQLC.
func NewGroupClaimPromptLoader(db *pgxpool.Pool) func(context.Context, string, string) []prompt.GroupClaim {
	q := sqlc.New(db)
	return func(ctx context.Context, groupID, agentID string) []prompt.GroupClaim {
		rows, err := q.ListLiveGroupClaims(ctx, groupID)
		if err != nil {
			return nil
		}
		claims := make([]prompt.GroupClaim, 0, len(rows))
		for _, claim := range rows {
			// The prompt speaks about peers; an agent does not need to be told
			// what it took itself.
			if claim.OwnerAgentID == agentID {
				continue
			}
			claims = append(claims, prompt.GroupClaim{Agent: groupClaimOwnerName(ctx, q, claim.OwnerAgentID), Subject: claim.Note, Age: groupClaimAge(claim.CreatedAt.UTC())})
		}
		return claims
	}
}

// NewGroupRosterPromptLoader returns the read-only prompt projection of a
// group's membership. It lives beside the claim loader for the same reason:
// the composition root does not reach through to SQLC.
func NewGroupRosterPromptLoader(db *pgxpool.Pool) func(context.Context, string, string) prompt.GroupRoster {
	q := sqlc.New(db)
	return func(ctx context.Context, groupID, agentID string) prompt.GroupRoster {
		members, err := q.ListGroupMembers(ctx, groupID)
		if err != nil {
			return prompt.GroupRoster{}
		}
		roster := prompt.GroupRoster{}
		for _, member := range members {
			name := groupClaimOwnerName(ctx, q, member.AgentID)
			if member.AgentID == agentID {
				roster.SelfName = name
				continue
			}
			roster.PeerNames = append(roster.PeerNames, name)
		}
		return roster
	}
}

// groupClaimOwnerName resolves a claim owner's display name, falling back to
// the agent id. Peers coordinate by name; ids mean nothing to a model.
func groupClaimOwnerName(ctx context.Context, q *sqlc.Queries, agentID string) string {
	if owner, err := q.GetAgent(ctx, agentID); err == nil && owner.Name != "" {
		return owner.Name
	}
	return agentID
}

// groupClaimAge rounds to the minute: a claim's exact second never changes a
// decision, and an exact timestamp invites the model to do arithmetic.
func groupClaimAge(createdAt time.Time) string {
	return max(time.Now().UTC().Sub(createdAt), 0).Round(time.Minute).String()
}

func (t *GroupClaimTools) Tools() []tools.Tool {
	if t == nil || t.q == nil {
		return nil
	}
	return []tools.Tool{groupClaimTool{t}, groupReleaseTool{t}, groupClaimsTool{t}}
}

type groupClaimTool struct{ owner *GroupClaimTools }

func (groupClaimTool) Definition() tools.Definition {
	return tools.Definition{Name: groupClaimToolName, Description: "Claim a concrete shared deliverable so peers do not duplicate it. Never claim an ordinary chat reply. ttl is clamped to 1 minute through 24 hours; default 10 minutes.", InputSchema: tools.MustInputSchema(`{"type":"object","required":["key"],"properties":{"key":{"type":"string"},"note":{"type":"string"},"ttl_seconds":{"type":"integer"}}}`)}
}

func (t groupClaimTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	groupID, agentID, err := groupClaimIdentity(ctx)
	if err != nil {
		return "", err
	}
	key, _ := args["key"].(string)
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("key is required")
	}
	if strings.EqualFold(key, "reply") || strings.EqualFold(key, "chat") {
		return "", fmt.Errorf("group_claim is for a shared deliverable, never an ordinary chat reply")
	}
	note, _ := args["note"].(string)
	ttl := defaultGroupClaimTTL
	if raw, ok := args["ttl_seconds"].(float64); ok {
		ttl = time.Duration(raw) * time.Second
	}
	if ttl < minimumGroupClaimTTL {
		ttl = minimumGroupClaimTTL
	}
	if ttl > maximumGroupClaimTTL {
		ttl = maximumGroupClaimTTL
	}
	row, err := t.owner.q.ClaimGroupWork(ctx, sqlc.ClaimGroupWorkParams{ID: uuid.Must(uuid.NewV7()).String(), GroupID: groupID, Key: key, OwnerAgentID: agentID, Note: note, LeaseUntil: time.Now().UTC().Add(ttl)})
	if err == nil {
		return tools.MarshalResult(map[string]any{"ok": true, "key": row.Key, "owner_agent_id": row.OwnerAgentID, "note": row.Note, "lease_until": row.LeaseUntil.UTC().Format(time.RFC3339)})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("claim group work: %w", err)
	}
	owner, getErr := t.owner.q.GetLiveGroupClaim(ctx, sqlc.GetLiveGroupClaimParams{GroupID: groupID, Key: key})
	if getErr != nil {
		return "", fmt.Errorf("read existing group claim: %w", getErr)
	}
	return tools.MarshalResult(map[string]any{"ok": false, "key": owner.Key, "owner_agent_id": owner.OwnerAgentID, "note": owner.Note, "lease_until": owner.LeaseUntil.UTC().Format(time.RFC3339)})
}

type groupReleaseTool struct{ owner *GroupClaimTools }

func (groupReleaseTool) Definition() tools.Definition {
	return tools.Definition{Name: groupReleaseToolName, Description: "Release one of your own group work claims.", InputSchema: tools.MustInputSchema(`{"type":"object","required":["key"],"properties":{"key":{"type":"string"}}}`)}
}

func (t groupReleaseTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	groupID, agentID, err := groupClaimIdentity(ctx)
	if err != nil {
		return "", err
	}
	key, _ := args["key"].(string)
	rows, err := t.owner.q.ReleaseGroupClaim(ctx, sqlc.ReleaseGroupClaimParams{GroupID: groupID, Key: strings.TrimSpace(key), OwnerAgentID: agentID})
	if err != nil {
		return "", err
	}
	return tools.MarshalResult(map[string]any{"ok": rows == 1})
}

type groupClaimsTool struct{ owner *GroupClaimTools }

func (groupClaimsTool) Definition() tools.Definition {
	return tools.Definition{Name: groupClaimsToolName, Description: "List active group work claims.", InputSchema: tools.MustInputSchema(`{"type":"object","properties":{}}`)}
}

func (t groupClaimsTool) Execute(ctx context.Context, _ map[string]any) (string, error) {
	groupID, agentID, err := groupClaimIdentity(ctx)
	if err != nil {
		return "", err
	}
	rows, err := t.owner.q.ListLiveGroupClaims(ctx, groupID)
	if err != nil {
		return "", err
	}
	// Raw rows carry uuids, lease timestamps and tombstone columns that cost
	// tokens and mean nothing to a model. It gets the same view the system
	// prompt gives, plus the key, which is the handle group_release needs --
	// so the caller's own claims stay listed, marked rather than hidden.
	claims := make([]map[string]any, 0, len(rows))
	for _, claim := range rows {
		claims = append(claims, map[string]any{
			"key":     claim.Key,
			"agent":   groupClaimOwnerName(ctx, t.owner.q, claim.OwnerAgentID),
			"subject": claim.Note,
			"age":     groupClaimAge(claim.CreatedAt.UTC()),
			"mine":    claim.OwnerAgentID == agentID,
		})
	}
	return tools.MarshalResult(map[string]any{"claims": claims})
}

func groupClaimIdentity(ctx context.Context) (string, string, error) {
	groupID, agentID := authz.GroupIDFromContext(ctx), authz.AgentIDFromContext(ctx)
	if groupID == "" || agentID == "" {
		return "", "", fmt.Errorf("group claim tools are available only in group turns")
	}
	return groupID, agentID, nil
}
