package skill

import "context"

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
