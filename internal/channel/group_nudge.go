package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/providers"
)

const (
	groupNudgeIdleMin       = 5 * time.Minute
	groupNudgeIdleMax       = 6 * time.Hour
	groupNudgeCooldown      = 45 * time.Minute
	groupNudgeFallbackDelay = 5 * time.Minute
	groupNudgeFallbackMax   = 3
	groupNudgeQueue         = "stella_group_nudge"
	groupNudgeInterval      = time.Minute
	groupNudgeTimeout       = 5 * time.Second
)

const groupNudgePrompt = `You detect genuinely stalled work in a group chat. Return JSON only: {"stalled":bool,"target":"member agent ID","reason":"short"}.
A thread is stalled only when there is a concrete unanswered ask or an explicitly waiting next step. Socially closed quiet threads are not stalls. Set stalled false for those. When stalled is true, target must be exactly one listed member agent ID.`

// GroupNudger creates canonical system messages for genuinely stalled group
// work. It is deliberately DB-backed: a CAS wins the cooldown before any
// append, so multiple process replicas cannot emit duplicate nudges.
type GroupNudger struct {
	db         *pgxpool.Pool
	q          *sqlc.Queries
	dispatcher interface{ Wake() }
	classifier GroupNudgeClassifier
	events     *GroupEventHub
	now        func() time.Time
}

// GroupNudgeRequest is the bounded, text-only classifier view of a quiet group.
type GroupNudgeRequest struct {
	GroupID, LastAuthor, LastAuthorType string
	Silence                             time.Duration
	Recent                              []string
	Claims                              []string
}

// GroupNudgeDecision is a model's bounded assessment of one quiet group.
// Stalled=false is a healthy decision, not a classifier outage.
type GroupNudgeDecision struct {
	Stalled bool
	Target  string
	Reason  string
}

// GroupNudgeClassifier uses the deployment system fast model to decide whether
// a quiet thread has a concrete unanswered ask and, if so, who should act.
type GroupNudgeClassifier interface {
	DecideNudge(context.Context, GroupNudgeRequest) (GroupNudgeDecision, error)
}

func (n *GroupNudger) SetClassifier(classifier GroupNudgeClassifier) { n.classifier = classifier }

// SetGroupEventHub projects the committed canonical nudge to live subscribers.
func (n *GroupNudger) SetGroupEventHub(events *GroupEventHub) { n.events = events }

func NewGroupNudger(db *pgxpool.Pool, dispatcher interface{ Wake() }) *GroupNudger {
	return &GroupNudger{db: db, q: sqlc.New(db), dispatcher: dispatcher, now: func() time.Time { return time.Now().UTC() }}
}

// RunOnce asks the classifier about every eligible group. Only a classifier
// outage can reach the deliberately narrow deterministic fallback.
func (n *GroupNudger) RunOnce(ctx context.Context) error {
	if n == nil || n.q == nil {
		return errors.New("group nudger unavailable")
	}
	now := n.now().UTC()
	candidates, err := n.q.ListGroupNudgeCandidate(ctx, sqlc.ListGroupNudgeCandidateParams{
		LatestBefore: now.Add(-groupNudgeIdleMin), EarliestAfter: now.Add(-groupNudgeIdleMax), Now: now,
	})
	if err != nil {
		return fmt.Errorf("list nudge candidates: %w", err)
	}
	if n.classifier != nil {
		fallback := false
		for _, candidate := range candidates {
			decision, available := n.classify(ctx, candidate, now)
			if !available {
				fallback = true
				continue
			}
			if !decision.Stalled {
				continue
			}
			if err := n.append(ctx, candidate.ID, decision.Target, decision.Reason, now, groupNudgeCooldown, false); err != nil {
				return err
			}
		}
		if !fallback {
			return nil
		}
	}
	if len(candidates) != 1 {
		return nil
	}
	candidate := candidates[0]
	if now.Sub(candidate.LastMessageAt) > 30*time.Minute ||
		(candidate.NudgeAt.Valid && candidate.NudgeAt.Time.UTC().After(now.Add(-groupNudgeFallbackDelay))) ||
		candidate.NudgeFallbackCount >= groupNudgeFallbackMax {
		return nil
	}
	claims, err := n.q.ListLiveGroupClaims(ctx, candidate.ID)
	if err != nil {
		return fmt.Errorf("list fallback nudge claims: %w", err)
	}
	if len(claims) != 1 || claims[0].OwnerAgentID == candidate.LastActorID {
		return nil
	}
	target := claims[0].OwnerAgentID
	return n.append(ctx, candidate.ID, target, claims[0].Note, now, groupNudgeFallbackDelay, true)
}

// classify asks the model whether one quiet group is stalled. The second return
// reports whether the classifier could answer at all: false routes the caller to
// the deterministic fallback, while a healthy "not stalled" is (decision, true).
func (n *GroupNudger) classify(ctx context.Context, candidate sqlc.ListGroupNudgeCandidateRow, now time.Time) (GroupNudgeDecision, bool) {
	claims, err := n.q.ListLiveGroupClaims(ctx, candidate.ID)
	if err != nil {
		return GroupNudgeDecision{}, false
	}
	rows, err := n.q.ListRecentGroupMessagesBeforeSeq(ctx, sqlc.ListRecentGroupMessagesBeforeSeqParams{GroupID: candidate.ID, BeforeSeq: candidate.NextSeq + 1, MaxCount: 6})
	if err != nil {
		return GroupNudgeDecision{}, false
	}
	recent, claimText := make([]string, 0, len(rows)), make([]string, 0, len(claims))
	for _, row := range rows {
		recent = append(recent, row.Content)
	}
	for _, claim := range claims {
		claimText = append(claimText, claim.OwnerAgentID+": "+claim.Note)
	}
	decision, err := n.classifier.DecideNudge(ctx, GroupNudgeRequest{GroupID: candidate.ID, LastAuthor: candidate.LastActorID, LastAuthorType: candidate.LastActorType, Silence: now.Sub(candidate.LastMessageAt), Recent: recent, Claims: claimText})
	if err != nil {
		return GroupNudgeDecision{}, false
	}
	if !decision.Stalled {
		return GroupNudgeDecision{}, true
	}
	decision.Target, decision.Reason = strings.TrimSpace(decision.Target), strings.TrimSpace(decision.Reason)
	if decision.Target == "" || decision.Reason == "" {
		return GroupNudgeDecision{}, false
	}
	members, err := n.q.ListGroupMembers(ctx, candidate.ID)
	if err != nil {
		return GroupNudgeDecision{}, false
	}
	for _, member := range members {
		if member.AgentID == decision.Target {
			return decision, true
		}
	}
	return GroupNudgeDecision{}, false
}

// append atomically wins the cooldown, persists the canonical system message,
// and creates its outbox. A cooldown without its message would lose work for
// 45 minutes after a transient database failure.
func (n *GroupNudger) append(ctx context.Context, groupID, target, note string, now time.Time, cooldown time.Duration, fallback bool) error {
	tx, err := n.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin group nudge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	if _, err := q.ClaimGroupNudge(ctx, sqlc.ClaimGroupNudgeParams{Now: nullTime(now), GroupID: groupID, CooldownBefore: nullTime(now.Add(-cooldown))}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("claim group nudge: %w", err)
	}
	if err := appdb.AdvisoryXactLock(ctx, tx, "gid:"+groupID); err != nil {
		return fmt.Errorf("lock group nudge append: %w", err)
	}
	result, err := eventlog.AppendToGroupWithQueries(ctx, q, groupID, eventlog.GroupMessage{ActorType: eventlog.ActorSystem, ActorID: "nudge", Content: fmt.Sprintf("%s, please continue %s.", target, note)})
	if err != nil {
		return fmt.Errorf("append nudge message: %w", err)
	}
	envelope, err := encodeGroupOutboxEnvelope(GroupOutboxEnvelope{ActorType: string(eventlog.ActorSystem), NudgeTarget: target})
	if err != nil {
		return err
	}
	if _, err := q.CreateGroupOutbox(ctx, sqlc.CreateGroupOutboxParams{ID: uuid.Must(uuid.NewV7()).String(), GroupMessageID: result.Message.ID, GroupID: groupID, Envelope: envelope, Status: "pending", LastError: ""}); err != nil {
		return fmt.Errorf("create nudge outbox: %w", err)
	}
	// AppendToGroupWithQueries bumps the group sequence, which resets the
	// counter for ordinary messages. Increment afterwards so the nudge itself
	// does not erase the cap that prevents an outage loop.
	if fallback {
		if _, err := q.IncrementGroupNudgeFallback(ctx, sqlc.IncrementGroupNudgeFallbackParams{GroupID: groupID, LimitCount: groupNudgeFallbackMax}); err != nil {
			return fmt.Errorf("increment nudge fallback: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit group nudge: %w", err)
	}
	if n.events != nil {
		n.events.Announce(result)
	}
	if n.dispatcher != nil {
		n.dispatcher.Wake()
	}
	return nil
}

// LLMGroupNudgeClassifier resolves only a system-scope member, so platform
// participants cannot spend a private member's provider credentials on nudges.
type LLMGroupNudgeClassifier struct {
	q        *sqlc.Queries
	load     SnapshotLoader
	build    StreamFuncBuilder
	complete CompleteFunc
}

func NewLLMGroupNudgeClassifier(db *pgxpool.Pool, load SnapshotLoader, build StreamFuncBuilder) *LLMGroupNudgeClassifier {
	return &LLMGroupNudgeClassifier{q: sqlc.New(db), load: load, build: build, complete: providers.Complete}
}

func (c *LLMGroupNudgeClassifier) DecideNudge(ctx context.Context, req GroupNudgeRequest) (GroupNudgeDecision, error) {
	if c == nil || c.q == nil || c.load == nil || c.build == nil || c.complete == nil {
		return GroupNudgeDecision{}, errors.New("group nudge classifier unavailable")
	}
	members, err := c.q.ListGroupMembers(ctx, req.GroupID)
	if err != nil {
		return GroupNudgeDecision{}, fmt.Errorf("list group members: %w", err)
	}
	modelAgentID := ""
	memberIDs := make([]string, 0, len(members))
	for _, member := range members {
		memberIDs = append(memberIDs, member.AgentID)
		agent, getErr := c.q.GetAgent(ctx, member.AgentID)
		if getErr == nil && agent.Scope == config.AgentScopeSystem && modelAgentID == "" {
			modelAgentID = agent.ID
		}
	}
	if modelAgentID == "" {
		return GroupNudgeDecision{}, errors.New("group nudge classifier has no system-scope member")
	}
	snap, err := c.load(ctx, modelAgentID)
	if err != nil || snap == nil {
		return GroupNudgeDecision{}, fmt.Errorf("load system agent snapshot: %w", err)
	}
	if strings.TrimSpace(snap.ModelFast) == "" {
		return GroupNudgeDecision{}, errors.New("system agent has no fast model")
	}
	model := snap.ResolveModelTier(config.ModelTierFast)
	creds := snap.ResolveProviderCreds(model.Provider)
	stream, err := c.build(ctx, classifierProviderType(snap, model.Provider, creds), creds)
	if err != nil || stream == nil {
		return GroupNudgeDecision{}, fmt.Errorf("build system fast-model stream: %w", err)
	}
	payload := fmt.Sprintf("Last author: %s\nLast author is human: %t\nSilence: %s\nMember agent IDs: %s\nLive claims:\n%s\nRecent messages:\n%s", req.LastAuthor, req.LastAuthorType == string(eventlog.ActorHuman), req.Silence.Round(time.Second), strings.Join(memberIDs, ", "), strings.Join(req.Claims, "\n"), strings.Join(req.Recent, "\n"))
	cctx, cancel := context.WithTimeout(ctx, groupNudgeTimeout)
	defer cancel()
	msg, err := c.complete(cctx, model, ai.Context{System: groupNudgePrompt, Messages: []ai.Message{ai.UserMessage{Content: payload}}}, ai.CompleteOptions{StreamOptions: ai.StreamOptions{Timeout: groupNudgeTimeout}}, stream)
	if err != nil {
		return GroupNudgeDecision{}, fmt.Errorf("complete group nudge classifier: %w", err)
	}
	var decision struct {
		Stalled bool   `json:"stalled"`
		Target  string `json:"target"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(ai.FlattenText(msg.Content))), &decision); err != nil {
		return GroupNudgeDecision{}, fmt.Errorf("decode group nudge classifier: %w", err)
	}
	return GroupNudgeDecision{Stalled: decision.Stalled, Target: strings.TrimSpace(decision.Target), Reason: strings.TrimSpace(decision.Reason)}, nil
}

type groupNudgeArgs struct{}

func (groupNudgeArgs) Kind() string { return groupNudgeQueue }

// GroupNudgeWorker is registered while the shared River client is assembled,
// then bound to channel coordination before that client starts.
type GroupNudgeWorker struct {
	river.WorkerDefaults[groupNudgeArgs]
	mu      sync.Mutex
	nudger  *GroupNudger
	started bool
	log     *slog.Logger
}

func NewGroupNudgeWorker() *GroupNudgeWorker {
	return &GroupNudgeWorker{log: slog.With("component", "group_nudge")}
}

func RegisterGroupNudgeWorker(workers *river.Workers, worker *GroupNudgeWorker) {
	river.AddWorker(workers, worker)
}

func GroupNudgeQueueConfig() (string, river.QueueConfig) {
	return groupNudgeQueue, river.QueueConfig{MaxWorkers: 1}
}

func (w *GroupNudgeWorker) Work(ctx context.Context, _ *river.Job[groupNudgeArgs]) error {
	w.mu.Lock()
	nudger := w.nudger
	w.mu.Unlock()
	if nudger == nil {
		return errors.New("group nudge worker ran before nudger bind")
	}
	if err := nudger.RunOnce(ctx); err != nil {
		w.log.Warn("group nudge pass failed; retrying next tick", "error", err)
	}
	return nil
}

func (w *GroupNudgeWorker) Bind(nudger *GroupNudger) error {
	if nudger == nil {
		return errors.New("group nudge worker requires a nudger")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("group nudge worker bind after periodic start")
	}
	if w.nudger != nil {
		return errors.New("group nudge worker already bound")
	}
	w.nudger = nudger
	return nil
}

func (w *GroupNudgeWorker) ValidateStartup() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.nudger == nil {
		return errors.New("group nudge worker requires a bound nudger before River start")
	}
	return nil
}

func (w *GroupNudgeWorker) StartPeriodic(client *river.Client[pgx.Tx]) (rivertype.PeriodicJobHandle, error) {
	if client == nil {
		return 0, errors.New("group nudge worker requires a River client")
	}
	w.mu.Lock()
	if w.nudger == nil {
		w.mu.Unlock()
		return 0, errors.New("group nudge worker requires a bound nudger before River start")
	}
	if w.started {
		w.mu.Unlock()
		return 0, errors.New("group nudge periodic already started")
	}
	w.started = true
	w.mu.Unlock()
	return client.PeriodicJobs().Add(river.NewPeriodicJob(
		river.PeriodicInterval(groupNudgeInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			return groupNudgeArgs{}, &river.InsertOpts{Queue: groupNudgeQueue, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateScheduled, rivertype.JobStateRetryable}}}
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	)), nil
}

func (w *GroupNudgeWorker) StopPeriodic(client *river.Client[pgx.Tx], handle rivertype.PeriodicJobHandle) {
	if client != nil {
		client.PeriodicJobs().Remove(handle)
	}
}
