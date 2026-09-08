package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/core/agentctx"
	"github.com/CherryHQ/stella/internal/memory"
)

// ChatAdmission is the synchronous lease for one turn. Begin registers the
// busy guard, Prepare performs slow runner/plugin work without the Service
// lifecycle locks, and Publish starts the asynchronous turn after a short
// publication recheck.
type ChatAdmission struct {
	rt                    *Runtime
	ctx                   context.Context
	info                  session.Info
	msg                   MessageContent
	co                    chatOptions
	activity              memory.Session
	turn                  *activeTurn
	out                   chan Event
	selection             runnerSelection
	prepared              bool
	published             bool
	preserveRunnerOnAbort bool
	abortOnce             sync.Once
}

// BeginChatAdmission registers the turn and runs the optional final ingress
// fence. It does no runner construction, filesystem access, or plugin I/O.
func (rt *Runtime) BeginChatAdmission(ctx context.Context, info session.Info, msg MessageContent, beforeStart func() error, opts ...Option) (admission *ChatAdmission, err error) {
	activity, err := info.MemoryScope()
	if err != nil {
		return nil, err
	}
	var turn *activeTurn
	defer func() {
		if recover() == nil {
			return
		}
		if turn != nil {
			turn.cancel()
			if rt.active.CompareAndDelete(info.ID, turn) {
				close(turn.done)
			}
		}
		admission = nil
		err = errors.New("chat admission failed")
	}()
	if rt.closed.Load() {
		return nil, errors.New("runtime is closed")
	}
	turnCtx, cancel := context.WithCancel(ctx)
	turn = &activeTurn{cancel: cancel, done: make(chan struct{}), ctx: turnCtx, info: info}
	if _, loaded := rt.active.LoadOrStore(info.ID, turn); loaded {
		cancel()
		return nil, fmt.Errorf("%w: session %s", ErrSessionBusy, info.ID)
	}
	if err := turnCtx.Err(); err != nil {
		turn.cancel()
		rt.active.CompareAndDelete(info.ID, turn)
		close(turn.done)
		return nil, err
	}
	if beforeStart != nil {
		if err := beforeStart(); err != nil {
			turn.cancel()
			rt.active.CompareAndDelete(info.ID, turn)
			close(turn.done)
			return nil, err
		}
	}
	var co chatOptions
	for _, option := range opts {
		option(&co)
	}
	turnCtx = memory.WithSessionID(turnCtx, info.ID)
	turnCtx = agentctx.WithTurnID(turnCtx, uuid.Must(uuid.NewV7()).String())
	turnCtx = withSessionIdentity(turnCtx, info)
	// An adapter context may carry the human authority that admitted a
	// session. Keep it out of the runtime turn until Prepare derives the
	// capability appropriate for this exact session, otherwise a cached runner
	// or a background turn could inherit the wrong principal.
	turnCtx = authz.ClearAuthority(turnCtx)
	return &ChatAdmission{
		rt:       rt,
		ctx:      turnCtx,
		info:     info,
		msg:      msg,
		co:       co,
		activity: activity,
		turn:     turn,
		out:      make(chan Event, 100),
	}, nil
}

// PrepareChatAdmission performs slow runner construction and captures the
// immutable selection. The caller must not hold Service lifecycle locks here.
func (rt *Runtime) PrepareChatAdmission(admission *ChatAdmission) (err error) {
	if admission == nil || admission.rt != rt {
		return errors.New("invalid chat admission")
	}
	if admission.prepared {
		return nil
	}
	defer func() {
		panicValue := recover()
		if panicValue == nil {
			return
		}
		rt.log.Error("chat publish panicked", "panic_type", fmt.Sprintf("%T", panicValue))
		admission.preserveRunnerOnAbort = false
		rt.AbortChatAdmission(admission)
		err = errors.New("chat preparation failed")
	}()
	if rt.closed.Load() {
		rt.AbortChatAdmission(admission)
		return errors.New("runtime is closed")
	}
	turnAuthority, hasAuthority, err := admissionTurnAuthority(admission.info, admission.co)
	if err != nil {
		rt.AbortChatAdmission(admission)
		return fmt.Errorf("derive turn authority: %w", err)
	}
	// Install exactly one authority for every downstream admission consumer.
	// Guests deliberately remain capability-empty; group and non-foreground
	// turns receive only their confined agent capability.
	admission.co.turnAuthority = turnAuthority
	admission.co.hasAuthority = hasAuthority
	admission.ctx = authz.ClearAuthority(admission.ctx)
	if hasAuthority {
		admission.ctx = authz.WithAuthority(admission.ctx, turnAuthority)
	}
	admission.turn.ctx = admission.ctx
	var (
		turnPluginContext    PluginContext
		hasTurnPluginContext bool
	)
	if rt.pluginContextBuilder != nil && hasAuthority {
		fresh, buildErr := rt.pluginContextBuilder(admission.ctx, turnAuthority, admission.info.AgentID)
		if buildErr != nil {
			rt.AbortChatAdmission(admission)
			return fmt.Errorf("build fresh plugin context: %w", buildErr)
		}
		turnPluginContext = fresh
		hasTurnPluginContext = true
		admission.ctx = WithPreparedPluginContext(admission.ctx, fresh)
		admission.turn.ctx = admission.ctx
	}
	for retries := 0; ; retries++ {
		expectedGeneration := rt.cache.factoryGenerationSnapshot()
		selection, selectErr := rt.getOrCreateReservedRunner(admission.ctx, admission.info, admission.co.model, admission.co.extraTools, expectedGeneration)
		if selectErr != nil {
			if errors.Is(selectErr, ErrRunnerFactoryChanged) && retries < maxFactoryGenerationRetries {
				continue
			}
			rt.AbortChatAdmission(admission)
			return fmt.Errorf("get runner: %w", selectErr)
		}
		if hasTurnPluginContext {
			selection.pluginContext = turnPluginContext
		}
		// Publish the reservation before optional per-turn preparation. Any
		// preparation error or panic must release this lease through the normal
		// admission abort path.
		admission.selection = selection
		admission.preserveRunnerOnAbort = true
		if preparer, ok := selection.runner.(TurnPreparer); ok {
			preparedCtx, preparedPluginContext, prepareErr := preparer.PrepareTurn(admission.ctx, selection.pluginContext)
			if prepareErr != nil {
				// A preparation failure belongs to this turn. Release the lease
				// while retaining the runner for the next admission; construction
				// failures and panics still take the quarantine path below.
				rt.AbortChatAdmission(admission)
				return fmt.Errorf("prepare runner turn: %w", prepareErr)
			}
			if preparedCtx == nil {
				rt.AbortChatAdmission(admission)
				return errors.New("prepare runner turn returned nil context")
			}
			admission.ctx = preparedCtx
			admission.turn.ctx = preparedCtx
			selection.pluginContext = preparedPluginContext
		}
		admission.selection = selection
		break
	}
	rt.capturePromptBuilders(&admission.selection)
	if admission.info.GuestID == "" && rt.skillTurnCapture != nil {
		captureCtx := authz.ClearAuthority(admission.ctx)
		if admission.co.hasAuthority {
			captureCtx = authz.WithAuthority(captureCtx, admission.co.turnAuthority)
		}
		captureCtx = authz.WithAgentID(captureCtx, admission.info.AgentID)
		captured, captureErr := rt.skillTurnCapture(captureCtx, admission.info, admission.selection.pluginContext)
		if captureErr != nil {
			rt.AbortChatAdmission(admission)
			return fmt.Errorf("capture skill turn view: %w", captureErr)
		}
		if captured == nil {
			rt.AbortChatAdmission(admission)
			return errors.New("capture skill turn view returned nil context")
		}
		admission.ctx = captured
		admission.turn.ctx = captured
	}
	admission.prepared = true
	return nil
}

// admissionTurnAuthority returns the capability that resource discovery and
// model-facing tools may use for one turn. A trusted foreground user
// capability is preserved when it matches the resolved session owner. Every
// other authenticated turn is reconstructed as a confined worker/group actor;
// no caller context authority is consulted.
func admissionTurnAuthority(info session.Info, options chatOptions) (authz.Authority, bool, error) {
	if info.GuestID != "" {
		return authz.Authority{}, false, nil
	}
	if info.GroupID != "" {
		authority, err := agentaccess.GroupAgentAuthority(info.GroupID, info.AgentID)
		return authority, err == nil, err
	}
	if options.hasAuthority && options.turnAuthority.Valid() &&
		options.turnAuthority.Kind() == authz.ActorUser &&
		string(options.turnAuthority.UserID()) == info.UserID && info.UserID != "" &&
		(info.Kind == string(session.KindMain) || info.Kind == string(session.KindChat)) &&
		info.Channel != string(session.ChannelWebhook) {
		return options.turnAuthority, true, nil
	}
	if info.UserID != "" && info.AgentID != "" {
		authority, err := agentaccess.WorkerAgentAuthority(info.UserID, info.AgentID)
		return authority, err == nil, err
	}
	return authz.Authority{}, false, nil
}

// AbortChatAdmission releases a prepared reservation and busy guard. It is
// safe to call after any failed phase; published turns own their cleanup.
func (rt *Runtime) AbortChatAdmission(admission *ChatAdmission) {
	if admission == nil || admission.rt != rt || admission.published {
		return
	}
	admission.abortOnce.Do(func() {
		if admission.prepared || admission.selection.session != nil {
			if admission.preserveRunnerOnAbort {
				rt.cache.releaseReservation(admission.selection.session)
			} else {
				rt.cache.abortReservedAdmission(admission.selection.session)
			}
		}
		if admission.turn != nil {
			admission.turn.cancel()
			if rt.active.CompareAndDelete(admission.info.ID, admission.turn) {
				close(admission.turn.done)
			}
		}
	})
}

// PublishChatAdmission performs the short publication recheck and starts the
// turn goroutines. The caller should hold the Service admission barrier only
// for this method, never for PrepareChatAdmission.
func (rt *Runtime) PublishChatAdmission(admission *ChatAdmission) (stream <-chan Event, err error) {
	if admission == nil || admission.rt != rt || !admission.prepared {
		return nil, errors.New("chat admission is not prepared")
	}
	defer func() {
		panicValue := recover()
		if panicValue == nil {
			return
		}
		rt.log.Error("chat publish panicked", "panic_type", fmt.Sprintf("%T", panicValue))
		if !admission.published {
			rt.AbortChatAdmission(admission)
		}
		stream = nil
		err = errors.New("chat publish failed")
	}()
	if rt.closed.Load() {
		rt.AbortChatAdmission(admission)
		return nil, errors.New("runtime is closed")
	}
	if err := admission.ctx.Err(); err != nil {
		rt.AbortChatAdmission(admission)
		return nil, err
	}
	if !rt.cache.validateReservation(admission.selection) {
		rt.AbortChatAdmission(admission)
		return nil, fmt.Errorf("chat admission expired: session %s", admission.info.ID)
	}
	value, ok := rt.active.Load(admission.info.ID)
	if !ok || value != admission.turn {
		rt.AbortChatAdmission(admission)
		return nil, fmt.Errorf("chat admission expired: session %s", admission.info.ID)
	}
	rt.markSessionTurnStarted(admission.ctx, admission.activity)
	inner := make(chan Event, 100)
	producerResult := make(chan memory.SessionTurnResult, 1)
	rt.hub.begin(admission.info.ID)
	rt.turns.begin()
	admission.published = true
	go rt.runChatProducer(admission, inner, producerResult)
	go rt.runChatForwarder(admission, inner, producerResult)
	return admission.out, nil
}

func (rt *Runtime) runChatProducer(admission *ChatAdmission, inner chan Event, producerResult chan memory.SessionTurnResult) {
	result := memory.SessionTurnSuccess
	defer rt.turns.end()
	defer func() {
		if p := recover(); p != nil {
			rt.log.Error("chat turn panicked", "session_id", admission.info.ID, "panic", p)
			result = memory.SessionTurnError
			safeClose(inner)
		}
		if result != memory.SessionTurnError && admission.ctx.Err() != nil {
			result = memory.SessionTurnCanceled
		}
		producerResult <- result
	}()
	rt.chatWithRunner(admission.ctx, inner, admission.info, admission.msg, admission.co, admission.selection)
}

func (rt *Runtime) runChatForwarder(admission *ChatAdmission, inner chan Event, producerResult chan memory.SessionTurnResult) {
	defer close(admission.out)
	defer close(admission.turn.done)
	defer admission.turn.cancel()
	defer rt.active.CompareAndDelete(admission.info.ID, admission.turn)
	defer rt.hub.end(admission.info.ID)
	result := memory.SessionTurnSuccess
	deliver := true
	for event := range inner {
		rt.hub.publish(admission.info.ID, event)
		if event.Err != nil {
			result = memory.SessionTurnError
		}
		if !deliver {
			continue
		}
		select {
		case admission.out <- event:
		case <-admission.ctx.Done():
			deliver = false
		}
	}
	producerOutcome := <-producerResult
	if result != memory.SessionTurnError {
		result = producerOutcome
	}
	rt.markSessionTurnCompleted(admission.ctx, admission.activity, result)
}
