package skills

import (
	"context"
	"errors"
)

// allowAllSkillReads is the permissive read authorizer test double: every DB
// skill read is allowed. It stands in for a fully-authorized actor so the
// existing tool tests exercise the read paths without a real Authorizer.
type allowAllSkillReads struct{}

func (allowAllSkillReads) BeginRead(context.Context) (SkillReadDecision, error) {
	return allowAllSkillReadDecision{}, nil
}

type allowAllSkillReadDecision struct{}

func (allowAllSkillReadDecision) AllowRead(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}

// denySkillReads denies every DB skill read, counting calls so a test can
// assert the authorizer was consulted.
type denySkillReads struct{ calls *int }

func (d denySkillReads) BeginRead(context.Context) (SkillReadDecision, error) {
	return denySkillReadDecision(d), nil
}

type denySkillReadDecision struct{ calls *int }

func (d denySkillReadDecision) AllowRead(context.Context, string, string, string, string) (bool, error) {
	if d.calls != nil {
		*d.calls++
	}
	return false, nil
}

// erroringSkillReads surfaces an unexpected authorization failure, which must
// propagate rather than silently drop a skill.
type erroringSkillReads struct{}

func (erroringSkillReads) BeginRead(context.Context) (SkillReadDecision, error) {
	return nil, errors.New("skill authorization unavailable")
}

// allowAllSkillWrites is the permissive write authorizer test double: every DB
// write is allowed. It stands in for a fully-authorized actor so the existing tool
// create/patch tests exercise the write paths.
type allowAllSkillWrites struct{}

func (allowAllSkillWrites) BeginWrite(context.Context) (SkillWriteDecision, error) {
	return allowAllSkillWriteDecision{}, nil
}

type allowAllSkillWriteDecision struct{}

func (allowAllSkillWriteDecision) AllowCreate(context.Context, string, string) error { return nil }
func (allowAllSkillWriteDecision) AllowWrite(context.Context, string) error          { return nil }

// denySkillWrites denies every DB write, counting calls so a test can assert the
// authorizer was consulted before the store mutation.
type denySkillWrites struct{ calls *int }

func (d denySkillWrites) BeginWrite(context.Context) (SkillWriteDecision, error) {
	return denySkillWriteDecision(d), nil
}

type denySkillWriteDecision struct{ calls *int }

func (d denySkillWriteDecision) AllowCreate(context.Context, string, string) error {
	if d.calls != nil {
		*d.calls++
	}
	return errors.New("skill access forbidden")
}

func (d denySkillWriteDecision) AllowWrite(context.Context, string) error {
	if d.calls != nil {
		*d.calls++
	}
	return errors.New("skill access forbidden")
}
