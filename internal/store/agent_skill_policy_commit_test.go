package store

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/skill/policy"
)

func TestCommitAgentSkillPolicyMarksCommitErrorOutcomeUnknown(t *testing.T) {
	next := policy.Policy{Disabled: []string{"builtin:stella"}}
	commitErr := errors.New("connection lost")
	got, err := commitAgentSkillPolicy(context.Background(), next, func(context.Context) error { return commitErr })
	if !errors.Is(err, policy.ErrCommitOutcomeUnknown) {
		t.Fatalf("commit error=%v; want ErrCommitOutcomeUnknown", err)
	}
	if !errors.Is(err, commitErr) {
		t.Fatalf("commit error=%v; want wrapped commit error", err)
	}
	if len(got.Disabled) != 1 || got.Disabled[0] != "builtin:stella" {
		t.Fatalf("returned policy=%#v; want intended next policy", got)
	}
}

func TestCommitAgentSkillPolicySuccess(t *testing.T) {
	next := policy.Policy{Disabled: []string{"builtin:stella"}}
	got, err := commitAgentSkillPolicy(context.Background(), next, func(context.Context) error { return nil })
	if err != nil || got.Disabled[0] != "builtin:stella" {
		t.Fatalf("commit result=%#v error=%v", got, err)
	}
}
