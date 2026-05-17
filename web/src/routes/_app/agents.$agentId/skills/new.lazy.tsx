import { createLazyFileRoute } from "@tanstack/react-router";
import { SkillNewPage } from "@/features/sessions/pages/SkillNewPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/skills/new")({
  component: SkillNewPage,
});
