import { createFileRoute } from "@tanstack/react-router";
import { validateSkillsSearch } from "@/lib/route-search";

export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/skills/")({
  validateSearch: validateSkillsSearch,
});
