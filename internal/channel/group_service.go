package channel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/eventlog"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// GroupService owns the Web group/channel application boundary: it authorizes
// every group operation against a trusted Authority, owns all group/member/
// message/outbox persistence, and returns transport-neutral values so the HTTP
// transport never reaches the query layer or sqlc. Group visibility is opaque:
// a non-owner (non-admin) sees a foreign group as absent, never forbidden.
//
// EventLog and Dispatcher are optional. When either is absent the group chat
// send path degrades to ErrGroupUnavailable; the read/CRUD path stays available.
type GroupService struct {
	db         *pgxpool.Pool
	agents     *agentaccess.Service
	resolver   *RuntimeResolver
	eventLog   *eventlog.Store
	dispatcher GroupDispatchRunner
	events     *GroupEventHub
	deletion   OwnerDeletion
}

// OwnerDeletion is the destructive group Home lifecycle boundary.
type OwnerDeletion interface {
	DeleteGroup(context.Context, string, string) error
}

// GroupServiceOption configures optional GroupService dependencies.
type GroupServiceOption func(*GroupService)

// WithOwnerDeletion supplies the destructive Home lifecycle for group deletion.
func WithOwnerDeletion(d OwnerDeletion) GroupServiceOption {
	return func(s *GroupService) { s.deletion = d }
}

// WithGroupEventHub attaches the channel-owned live projection hub.
func WithGroupEventHub(h *GroupEventHub) GroupServiceOption {
	return func(s *GroupService) { s.events = h }
}

// GroupDispatchRunner wakes durable group work after an accepted ingest.
type GroupDispatchRunner interface {
	Wake()
	AbortGroupTurn(groupID, agentID string) bool
}

// webGroupPlatform is the platform value the Web group surface writes: it names
// the origin of a message id, so a browser's client_message_id can never be
// mistaken for a platform message id in the same group.
const webGroupPlatform = "web"

// groupReplayWindow bounds one stream replay. Raise it when clients need deeper
// scrollback on connect; paging history is the /messages endpoint's job.
const groupReplayWindow = 500

// NewGroupService builds the group boundary over the pool, the Agent PEP (agent
// use authorization), and the runtime resolver (agent-name projection). eventLog
// and dispatcher may be nil, degrading only the send path to 503.
func NewGroupService(db *pgxpool.Pool, agents *agentaccess.Service, resolver *RuntimeResolver, eventLog *eventlog.Store, dispatcher GroupDispatchRunner, opts ...GroupServiceOption) *GroupService {
	s := &GroupService{
		db:         db,
		agents:     agents,
		resolver:   resolver,
		eventLog:   eventLog,
		dispatcher: dispatcher,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Opaque boundary errors. Agent-use denials surface the Agent PEP's own
// sentinels (agentaccess.ErrNotFound / ErrForbidden) so the transport keeps the
// historical per-agent bodies; the transport maps all of these to HTTP.
var (
	// ErrGroupNotFound is returned for a missing group and for a group the caller
	// may not see — visibility is opaque, so the two are indistinguishable.
	ErrGroupNotFound = errors.New("group not found")
	// ErrGroupUnavailable reports the send path is not wired (no event log or
	// dispatcher). Read/CRUD endpoints never return it.
	ErrGroupUnavailable = errors.New("group chat unavailable")
	// ErrLastGroupMember guards the last-member invariant: a group always keeps at
	// least one agent.
	ErrLastGroupMember = errors.New("cannot remove the last member")
	// ErrInvalidPage reports pagination arguments outside the representable range
	// (negative/oversized offset or non-positive/oversized limit) before they are
	// narrowed to the query layer's int32 columns.
	ErrInvalidPage = errors.New("invalid pagination")
	// ErrInvalidCaps reports a dispatch cap outside its permitted range.
	ErrInvalidCaps = errors.New("invalid dispatch cap")
)

// validatePage rejects paging arguments that would overflow or invert once
// narrowed to the int32 LIMIT/OFFSET columns. It is defense in depth: the HTTP
// transport already bounds page_size, but a crafted page token or a direct
// (tool) caller could still supply an out-of-range offset.
func validatePage(offset, limit int) error {
	if offset < 0 || limit <= 0 || int64(offset)+int64(limit) > math.MaxInt32 {
		return ErrInvalidPage
	}
	return nil
}

// Transport-neutral group values.

// Group is a Web group's durable state plus its message-derived last-active
// time (nil when the group has no messages yet).
type Group struct {
	ID                        string
	GroupName                 string
	Platform                  string
	CreatedByUserID           *string
	LastActive                *time.Time
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	AgentChainHardLimit       int
	MaxAgentPostsPerMinute    int
	MaxRepliesPerHumanTrigger int
	HoldLimit                 int
}

type GroupDispatchCaps struct {
	AgentChainHardLimit       int
	MaxAgentPostsPerMinute    int
	MaxRepliesPerHumanTrigger int
	HoldLimit                 int
}

// validate names the offending cap. A caller that sent four of them cannot
// otherwise tell which one the server refused, and the spec's minimum/maximum
// are documentation only: nothing validates requests against it.
func (c GroupDispatchCaps) validate() error {
	switch {
	case c.AgentChainHardLimit < 1 || c.AgentChainHardLimit > 100:
		return fmt.Errorf("%w: agent_chain_hard_limit must be between 1 and 100", ErrInvalidCaps)
	case c.MaxAgentPostsPerMinute < 1 || c.MaxAgentPostsPerMinute > 1000:
		return fmt.Errorf("%w: max_agent_posts_per_minute must be between 1 and 1000", ErrInvalidCaps)
	case c.MaxRepliesPerHumanTrigger < 1 || c.MaxRepliesPerHumanTrigger > 100:
		return fmt.Errorf("%w: max_replies_per_human_trigger must be between 1 and 100", ErrInvalidCaps)
	case c.HoldLimit < 0 || c.HoldLimit > 20:
		return fmt.Errorf("%w: hold_limit must be between 0 and 20", ErrInvalidCaps)
	}
	return nil
}

// GroupMemberDetail is one agent member of a group with its display name
// projected (nil when the agent binding is stale/unreadable).
type GroupMemberDetail struct {
	GroupID        string
	AgentID        string
	ReplyChannelID string
	AgentName      *string
	CreatedAt      time.Time
}

// GroupMessageItem is one appended group message row.
type GroupMessageItem struct {
	ID             string
	GroupID        string
	Seq            int
	ActorType      string
	ActorID        string
	Content        string
	Reasoning      *string
	AgentSessionID *string
	DeliveryState  string
	CreatedAt      time.Time
}

// SubscribeEvents authorizes the durable replay and then attaches to the
// best-effort live hub. Callers must replay first, before subscribing.
func (a *GroupAccess) SubscribeEvents(ctx context.Context, groupID string) (<-chan GroupEvent, func(), error) {
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return nil, nil, err
	}
	if a.svc.events == nil {
		return nil, nil, ErrGroupUnavailable
	}
	ch, cancel := a.svc.events.Subscribe(groupID)
	return ch, cancel, nil
}

// MessagesAfterSeq replays the newest window of canonical rows, in ascending
// sequence order. A group longer than the window drops its oldest messages from
// the replay, never its newest: the stream exists to show what just happened.
func (a *GroupAccess) MessagesAfterSeq(ctx context.Context, groupID string, sinceSeq int64) ([]GroupMessageItem, error) {
	if sinceSeq < 0 {
		return nil, ErrInvalidPage
	}
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return nil, err
	}
	rows, err := a.q().ListLatestGroupMessagesAfterSeq(ctx, sqlc.ListLatestGroupMessagesAfterSeqParams{GroupID: groupID, MinSeq: sinceSeq, BatchLimit: groupReplayWindow})
	if err != nil {
		return nil, fmt.Errorf("replay group messages: %w", err)
	}
	out := make([]GroupMessageItem, len(rows))
	for i, m := range rows {
		out[i] = GroupMessageItem{ID: m.ID, GroupID: m.GroupID, Seq: int(m.Seq), ActorType: m.ActorType, ActorID: m.ActorID, Content: m.Content, Reasoning: strPtr(m.Reasoning), AgentSessionID: strPtr(m.AgentSessionID), DeliveryState: m.DeliveryState, CreatedAt: m.CreatedAt.UTC()}
	}
	return out, nil
}

// GroupAccess is one authorized group session: every method decides against the
// trusted Authority the composition minted from a verified session. Request
// path/body fields never contribute to it.
type GroupAccess struct {
	svc       *GroupService
	authority authz.Authority
}

// Begin opens a group session for a trusted Authority.
func (s *GroupService) Begin(_ context.Context, authority authz.Authority) (*GroupAccess, error) {
	if !authority.Valid() {
		return nil, ErrGroupNotFound
	}
	return &GroupAccess{svc: s, authority: authority}, nil
}

func (a *GroupAccess) q() *sqlc.Queries { return sqlc.New(a.svc.db) }

func (a *GroupAccess) actorUserID() string { return string(a.authority.UserID()) }

// requireOwner loads a group and enforces opaque owner/admin visibility: a
// non-owner non-admin caller sees ErrGroupNotFound, never a forbidden signal.
func (a *GroupAccess) requireOwner(ctx context.Context, groupID string) (sqlc.CtxGroupState, error) {
	g, err := a.q().GetGroupStateByID(ctx, groupID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.CtxGroupState{}, ErrGroupNotFound
		}
		return sqlc.CtxGroupState{}, fmt.Errorf("get group state: %w", err)
	}
	if err := a.checkVisible(g); err != nil {
		return sqlc.CtxGroupState{}, err
	}
	return g, nil
}

// checkVisible enforces opaque owner/admin visibility on an already-loaded group
// row so the same rule applies to both the pooled read path (requireOwner) and
// the row-locked transaction path (RemoveMember).
func (a *GroupAccess) checkVisible(g sqlc.CtxGroupState) error {
	if !a.authority.IsAdmin() && (!g.CreatedByUserID.Valid || g.CreatedByUserID.String != a.actorUserID()) {
		return ErrGroupNotFound
	}
	return nil
}

// List returns the caller's own groups (owner-scoped even for admins, matching
// the roster view), newest activity first. offset/limit are transport paging
// mechanics; the caller passes limit+1 to detect a further page.
func (a *GroupAccess) List(ctx context.Context, offset, limit int) ([]Group, error) {
	if err := validatePage(offset, limit); err != nil {
		return nil, err
	}
	rows, err := a.q().ListGroupsByUser(ctx, sqlc.ListGroupsByUserParams{
		UserID:      pgtype.Text{String: a.actorUserID(), Valid: true},
		LimitCount:  int32(limit),
		OffsetCount: int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	out := make([]Group, len(rows))
	for i, r := range rows {
		out[i] = Group{
			ID:                        r.ID,
			GroupName:                 r.GroupName,
			Platform:                  r.Platform,
			CreatedByUserID:           textPtr(r.CreatedByUserID),
			LastActive:                timePtr(r.LastActive),
			CreatedAt:                 r.CreatedAt.UTC(),
			UpdatedAt:                 r.UpdatedAt.UTC(),
			AgentChainHardLimit:       int(r.AgentChainHardLimit),
			MaxAgentPostsPerMinute:    int(r.MaxAgentPostsPerMinute),
			MaxRepliesPerHumanTrigger: int(r.MaxRepliesPerHumanTrigger),
			HoldLimit:                 int(r.HoldLimit),
		}
	}
	return out, nil
}

// Create provisions a Web group owned by the caller and seeds its agent members.
// Every agent is authorized for use first; a single denial aborts before any
// write. The group state, every member's web reply channel, and every membership
// row are written in one transaction, so any member/channel write failure rolls
// the whole group back — no half-provisioned group can survive.
func (a *GroupAccess) Create(ctx context.Context, name string, agentIDs []string) (Group, error) {
	for _, agentID := range agentIDs {
		if _, err := a.svc.agents.Use(ctx, a.authority, agentID); err != nil {
			return Group{}, err
		}
	}

	tx, err := a.svc.db.Begin(ctx)
	if err != nil {
		return Group{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)

	groupID := uuid.Must(uuid.NewV7()).String()
	g, err := q.CreateGroupState(ctx, sqlc.CreateGroupStateParams{
		ID:               groupID,
		Platform:         webGroupPlatform,
		PlatformGroupID:  groupID,
		PlatformThreadID: "",
		GroupName:        name,
		CreatedByUserID:  pgtype.Text{String: a.actorUserID(), Valid: true},
	})
	if err != nil {
		return Group{}, fmt.Errorf("create group state: %w", err)
	}
	for _, agentID := range agentIDs {
		if _, err := a.addMemberRow(ctx, q, groupID, agentID); err != nil {
			return Group{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Group{}, fmt.Errorf("commit: %w", err)
	}
	return a.groupValue(ctx, g)
}

// Get returns one group the caller may see, with its message-derived activity.
func (a *GroupAccess) Get(ctx context.Context, groupID string) (Group, error) {
	g, err := a.requireOwner(ctx, groupID)
	if err != nil {
		return Group{}, err
	}
	return a.groupValue(ctx, g)
}

// UpdateName renames a group the caller owns.
func (a *GroupAccess) UpdateName(ctx context.Context, groupID, name string) (Group, error) {
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return Group{}, err
	}
	g, err := a.q().UpdateGroupName(ctx, sqlc.UpdateGroupNameParams{GroupName: name, ID: groupID})
	if err != nil {
		return Group{}, fmt.Errorf("update group name: %w", err)
	}
	return a.groupValue(ctx, g)
}

func (a *GroupAccess) UpdateCaps(ctx context.Context, groupID string, caps GroupDispatchCaps) (Group, error) {
	if err := caps.validate(); err != nil {
		return Group{}, err
	}
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return Group{}, err
	}
	g, err := a.q().SetGroupDispatchCaps(ctx, sqlc.SetGroupDispatchCapsParams{ID: groupID, AgentChainHardLimit: int32(caps.AgentChainHardLimit), MaxAgentPostsPerMinute: int32(caps.MaxAgentPostsPerMinute), MaxRepliesPerHumanTrigger: int32(caps.MaxRepliesPerHumanTrigger), HoldLimit: int32(caps.HoldLimit)})
	if err != nil {
		return Group{}, fmt.Errorf("update group caps: %w", err)
	}
	return a.groupValue(ctx, g)
}

// Delete removes a group the caller owns.
func (a *GroupAccess) Delete(ctx context.Context, groupID string) error {
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return err
	}
	if a.svc.deletion == nil {
		return fmt.Errorf("%w: delete group lifecycle is not wired", ErrGroupUnavailable)
	}
	if err := a.svc.deletion.DeleteGroup(ctx, groupID, a.actorUserID()); err != nil {
		return fmt.Errorf("delete group state: %w", err)
	}
	return nil
}

// Members lists a group's agent members with display names projected.
func (a *GroupAccess) Members(ctx context.Context, groupID string) ([]GroupMemberDetail, error) {
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return nil, err
	}
	rows, err := a.q().ListGroupMembers(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	out := make([]GroupMemberDetail, len(rows))
	for i, m := range rows {
		out[i] = a.memberDetail(ctx, m)
	}
	return out, nil
}

// AddMember adds an authorized agent to a group the caller owns.
func (a *GroupAccess) AddMember(ctx context.Context, groupID, agentID string) (GroupMemberDetail, error) {
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return GroupMemberDetail{}, err
	}
	if _, err := a.svc.agents.Use(ctx, a.authority, agentID); err != nil {
		return GroupMemberDetail{}, err
	}
	m, err := a.addMemberRow(ctx, a.q(), groupID, agentID)
	if err != nil {
		return GroupMemberDetail{}, err
	}
	return a.memberDetail(ctx, m), nil
}

// RemoveMember drops an agent from a group the caller owns, holding the
// last-member invariant. The ownership check, member count, and delete run in one
// transaction that takes a row lock on the group (SELECT ... FOR UPDATE), so
// concurrent removals on the same group serialize: two callers each removing one
// of the last two members cannot both observe count > 1, and exactly one member
// always survives.
func (a *GroupAccess) RemoveMember(ctx context.Context, groupID, agentID string) error {
	tx, err := a.svc.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)

	// Serialize concurrent removals on this group by locking its registry row
	// before counting; the second remover blocks here until the first commits and
	// then observes the reduced count.
	g, err := q.GetGroupStateByIDForUpdate(ctx, groupID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGroupNotFound
		}
		return fmt.Errorf("lock group state: %w", err)
	}
	if err := a.checkVisible(g); err != nil {
		return err
	}

	count, err := q.CountGroupMembers(ctx, groupID)
	if err != nil {
		return fmt.Errorf("count group members: %w", err)
	}
	if count <= 1 {
		return ErrLastGroupMember
	}
	if err := q.RemoveGroupMember(ctx, sqlc.RemoveGroupMemberParams{GroupID: groupID, AgentID: agentID}); err != nil {
		return fmt.Errorf("remove group member: %w", err)
	}
	return tx.Commit(ctx)
}

// Messages lists a group's appended messages, newest first. offset/limit are
// transport paging mechanics; the caller passes limit+1 to detect a further page.
func (a *GroupAccess) Messages(ctx context.Context, groupID string, offset, limit int) ([]GroupMessageItem, error) {
	if err := validatePage(offset, limit); err != nil {
		return nil, err
	}
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return nil, err
	}
	rows, err := a.q().ListGroupMessagesPaginated(ctx, sqlc.ListGroupMessagesPaginatedParams{
		GroupID:     groupID,
		LimitCount:  int32(limit),
		OffsetCount: int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list group messages: %w", err)
	}
	out := make([]GroupMessageItem, len(rows))
	for i, m := range rows {
		out[i] = GroupMessageItem{ID: m.ID, GroupID: m.GroupID, Seq: int(m.Seq), ActorType: m.ActorType, ActorID: m.ActorID, Content: m.Content, Reasoning: strPtr(m.Reasoning), AgentSessionID: strPtr(m.AgentSessionID), DeliveryState: m.DeliveryState, CreatedAt: m.CreatedAt.UTC()}
	}
	return out, nil
}

// PreparedSend is the outcome of preparing a Web group send. Exactly one of the
// three states holds:
//   - Command: a group slash command was intercepted; Reply carries the plain text.
//   - Deduplicated: the append collapsed onto an existing row (idempotent replay).
//   - otherwise: a fresh message was appended with its outbox; the caller wakes
//     the worker pool and the turn runs asynchronously.
type PreparedSend struct {
	Command      bool
	Reply        string
	Deduplicated bool
	MessageSeq   int
}

// PrepareSend authorizes the caller as the group owner, intercepts group slash
// commands, then appends the human message and creates its dispatch outbox
// inside one transaction (dedup preserved). The appended actor is the authenticated
// user id (invariant #308): the dispatcher trusts it as the current-speaker
// profile target, so it must never be a client-supplied value.
func (a *GroupAccess) PrepareSend(ctx context.Context, groupID, content, clientMessageID string) (PreparedSend, error) {
	if a.svc.eventLog == nil || a.svc.dispatcher == nil {
		return PreparedSend{}, ErrGroupUnavailable
	}
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return PreparedSend{}, err
	}

	members, err := a.q().ListGroupMembers(ctx, groupID)
	if err != nil {
		return PreparedSend{}, fmt.Errorf("list group members: %w", err)
	}

	if reply, handled := a.groupCommandReply(ctx, groupID, content, clientMessageID, members); handled {
		return PreparedSend{Command: true, Reply: reply}, nil
	}

	// Unified entry: triple resolve + dedup via AppendGroupMessage. Empty
	// client_message_id disables tier-1 dedup (no fake UUID). The outbox is created
	// inside the same transaction so a fresh message always has a claimable row.
	appendResult, err := a.svc.eventLog.AppendGroupMessage(ctx, eventlog.Message{
		Platform:          webGroupPlatform,
		PlatformGroupID:   groupID,
		PlatformThreadID:  "",
		ActorType:         eventlog.ActorHuman,
		ActorID:           a.actorUserID(),
		PlatformMessageID: clientMessageID,
		Content:           content,
	}, eventlog.WithOnInserted(func(ctx context.Context, q *sqlc.Queries, result eventlog.AppendResult) error {
		mentions := parseWebMentions(content, members)
		envelope, err := EncodeGroupOutboxEnvelope(mentions)
		if err != nil {
			return fmt.Errorf("encode outbox envelope: %w", err)
		}
		if _, err := q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{
			ID:             uuid.Must(uuid.NewV7()).String(),
			GroupMessageID: result.Message.ID,
			GroupID:        result.GroupID,
			Envelope:       envelope,
			Status:         "pending",
			AttemptCount:   0,
			LeaseUntil:     pgtype.Timestamptz{},
			NextAttemptAt:  pgtype.Timestamptz{},
			LastError:      "",
		}); err != nil {
			return fmt.Errorf("create group outbox: %w", err)
		}
		return nil
	}))
	if err != nil {
		return PreparedSend{}, err
	}
	if !appendResult.Inserted {
		return PreparedSend{Deduplicated: true}, nil
	}

	return PreparedSend{MessageSeq: int(appendResult.Message.Seq)}, nil
}

// Wake makes a newly persisted outbox immediately visible to the worker pool.
func (a *GroupAccess) Wake() {
	if a.svc.dispatcher != nil {
		a.svc.dispatcher.Wake()
	}
}

// AbortGroupTurn authorizes cancellation against the group before addressing
// the dispatcher's per-agent session slot. A missing active turn is a
// successful, idempotent abort.
func (a *GroupAccess) AbortGroupTurn(ctx context.Context, groupID, agentID string) error {
	if _, err := a.requireOwner(ctx, groupID); err != nil {
		return err
	}
	if a.svc.dispatcher == nil {
		return ErrGroupUnavailable
	}
	a.svc.dispatcher.AbortGroupTurn(groupID, agentID)
	return nil
}

// addMemberRow creates the agent's web reply channel (idempotent) and its group
// membership row, returning the created membership.
func (a *GroupAccess) addMemberRow(ctx context.Context, q *sqlc.Queries, groupID, agentID string) (sqlc.ChannelGroupMember, error) {
	if err := q.CreateWebChannelIfNotExists(ctx, sqlc.CreateWebChannelIfNotExistsParams{
		ID:      webChannelID(agentID),
		AgentID: pgtype.Text{String: agentID, Valid: true},
	}); err != nil {
		return sqlc.ChannelGroupMember{}, fmt.Errorf("create web channel: %w", err)
	}
	m, err := q.AddGroupMember(ctx, sqlc.AddGroupMemberParams{
		GroupID:        groupID,
		AgentID:        agentID,
		ReplyChannelID: webChannelID(agentID),
	})
	if err != nil {
		return sqlc.ChannelGroupMember{}, fmt.Errorf("add group member: %w", err)
	}
	return m, nil
}

// groupValue converts a group state row and folds in its message-derived
// last-active time so single-resource responses match the list endpoint.
func (a *GroupAccess) groupValue(ctx context.Context, g sqlc.CtxGroupState) (Group, error) {
	out := Group{
		ID:                        g.ID,
		GroupName:                 g.GroupName,
		Platform:                  g.Platform,
		CreatedByUserID:           textPtr(g.CreatedByUserID),
		CreatedAt:                 g.CreatedAt.UTC(),
		UpdatedAt:                 g.UpdatedAt.UTC(),
		AgentChainHardLimit:       int(g.AgentChainHardLimit),
		MaxAgentPostsPerMinute:    int(g.MaxAgentPostsPerMinute),
		MaxRepliesPerHumanTrigger: int(g.MaxRepliesPerHumanTrigger),
		HoldLimit:                 int(g.HoldLimit),
	}
	// Absent messages default last-active to the update time, matching the prior
	// single-resource semantics before the message-derived override.
	updated := g.UpdatedAt.UTC()
	out.LastActive = &updated
	if la, err := a.q().GetGroupLastActive(ctx, g.ID); err == nil {
		if t := la.UTC(); !t.IsZero() {
			out.LastActive = &t
		}
	}
	return out, nil
}

func (a *GroupAccess) memberDetail(ctx context.Context, m sqlc.ChannelGroupMember) GroupMemberDetail {
	detail := GroupMemberDetail{
		GroupID:        m.GroupID,
		AgentID:        m.AgentID,
		ReplyChannelID: m.ReplyChannelID,
		CreatedAt:      m.CreatedAt.UTC(),
	}
	if name, ok := a.svc.resolver.AgentName(ctx, m.AgentID); ok {
		detail.AgentName = &name
	}
	return detail
}

// webChannelID is the canonical Web reply-channel id for an agent: the durable
// binding a Web group member replies through.
func webChannelID(agentID string) string { return "web:" + agentID }

// groupCommandReply intercepts group slash commands before the event-log append,
// returning the plain reply and true. Any other input (including other slash
// commands) falls through as a normal message. Interception happens here rather
// than after the append so a command never becomes part of group context —
// which matters most for `/new`, whose whole purpose is to clear that context.
// clientMessageID is the browser's idempotency token for this send. Only `/new`
// needs it — the other commands change nothing, so answering a retry twice costs
// nothing — but it has to reach here because the append that would normally
// dedup the message is exactly what interception skips.
func (a *GroupAccess) groupCommandReply(ctx context.Context, groupID, content, clientMessageID string, members []sqlc.ChannelGroupMember) (string, bool) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/config":
		return "⚠️ /config is not available in group chats. Please use it in a direct message.", true
	case "/compact":
		// Group history is assembled from the group event log, not per-agent LCM
		// conversations, so compaction does not apply here.
		return pkgchannel.GroupCompactUnsupportedMessage, true
	case "/new":
		// A group's context is shared by every member, so no member's chat
		// command may clear it. Refuse explicitly rather than silently.
		return pkgchannel.GroupNewSessionUnsupportedMessage, true
	default:
		return "", false
	}
}

// textPtr maps a nullable text column to *string (nil when NULL).
func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

// strPtr maps an empty string to nil, matching the transport's prior ptrStr for
// optional message fields.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// timePtr maps a zero time to nil, otherwise a UTC pointer.
func timePtr(t time.Time) *time.Time {
	u := t.UTC()
	if u.IsZero() {
		return nil
	}
	return &u
}
