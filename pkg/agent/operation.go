package agent

import "context"

// OperationCheck is the runtime-owned fence consulted immediately before a
// model, tool, or code-mode operation. The public agent loop cannot import the
// internal AgentRun package, so the runtime supplies this narrow hook through
// context.
type OperationCheck func(context.Context) error

// OperationBinder restores the runtime-owned values (the durable AgentRun
// guard in production) onto a context returned by a hook.
type OperationBinder func(context.Context) context.Context

type operationCheckKey struct{}

type operationFence struct {
	check OperationCheck
	bind  OperationBinder
}

// WithOperationCheck carries a fail-closed operation fence into a Runner.
func WithOperationCheck(ctx context.Context, check OperationCheck) context.Context {
	if check == nil {
		return ctx
	}
	return context.WithValue(ctx, operationCheckKey{}, operationFence{check: check})
}

// WithOperationContext carries both the operation check and the runtime-owned
// context decorator. Hooks may return a fresh context, but cannot remove the
// durable guard needed by model, tool, and sandbox writes.
func WithOperationContext(ctx context.Context, check OperationCheck, bind OperationBinder) context.Context {
	if check == nil {
		return ctx
	}
	return context.WithValue(ctx, operationCheckKey{}, operationFence{check: check, bind: bind})
}

// CheckOperation executes the current runtime fence, if one was installed.
// Contexts without a runtime lease remain valid for standalone agent-loop
// callers and tests.
func CheckOperation(ctx context.Context) error {
	fence, _ := ctx.Value(operationCheckKey{}).(operationFence)
	if fence.check == nil {
		return nil
	}
	return fence.check(ctx)
}

// inheritOperationCheck copies the engine-owned fence onto a hook-derived
// context. Hooks may return a fresh context for tracing or values, but they
// must not be able to remove the runtime's ownership check from a later tool
// dispatch.
func inheritOperationCheck(ctx, source context.Context) context.Context {
	fence, _ := source.Value(operationCheckKey{}).(operationFence)
	if fence.check == nil {
		return ctx
	}
	if fence.bind != nil {
		ctx = fence.bind(ctx)
	}
	return context.WithValue(ctx, operationCheckKey{}, fence)
}
