import { createFileRoute } from "@tanstack/react-router";
import { SkillNewPage } from "@/features/sessions/pages/SkillNewPage";

export const Route = createFileRoute("/_app/agents/$agentId/skills/new")({
  component: SkillNewPage,
});
