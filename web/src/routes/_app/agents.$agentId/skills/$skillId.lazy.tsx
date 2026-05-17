import { createLazyFileRoute } from "@tanstack/react-router";
import { SkillEditPage } from "@/features/sessions/pages/SkillEditPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/skills/$skillId")({
  component: SkillEditPage,
});
