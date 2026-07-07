package reflect

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/skills"
)

type reflectSkillWriter interface {
	CreateReflectOwnedUserAgentSkill(ctx context.Context, in skills.ReflectSkillCreate) (skills.Skill, error)
	PatchReflectOwnedUserAgentSkill(ctx context.Context, in skills.ReflectSkillPatch) (skills.Skill, error)
}

func executeSkillReconciliationPlan(ctx context.Context, writer reflectSkillWriter, userID string, agentID string, bundle skillRelatedBundle, plan skillReconciliationPlan) ([]skills.Skill, error) {
	if writer == nil {
		return nil, fmt.Errorf("skill reconciliation: reflect skill writer is required")
	}
	if err := validateSkillReconciliationPlan(bundle, plan); err != nil {
		return nil, err
	}
	written := make([]skills.Skill, 0, len(plan.Operations))
	// V1 executes skill writes one operation at a time. Each store call owns its
	// transaction, and a later failure leaves earlier writes committed while the
	// skill-line watermark stays unadvanced for retry/reconciliation on the next
	// review cycle.
	for _, op := range plan.Operations {
		switch op.Operation {
		case skillOperationNoop:
			continue
		case skillOperationCreate:
			skill, err := writer.CreateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillCreate{
				UserID:          userID,
				AgentID:         agentID,
				Name:            op.Name,
				Description:     op.Description,
				MainFileContent: op.MainFileContent,
			})
			if err != nil {
				return nil, err
			}
			written = append(written, skill)
		case skillOperationPatch:
			mainFile := op.MainFileContent
			patch := skills.ReflectSkillPatch{
				ID:              op.TargetSkillID,
				UserID:          userID,
				AgentID:         agentID,
				ExpectedVersion: op.ExpectedSkillVersion,
				MainFileContent: &mainFile,
			}
			if op.Description != "" {
				description := op.Description
				patch.Description = &description
			}
			skill, err := writer.PatchReflectOwnedUserAgentSkill(ctx, patch)
			if err != nil {
				return nil, err
			}
			written = append(written, skill)
		}
	}
	return written, nil
}
