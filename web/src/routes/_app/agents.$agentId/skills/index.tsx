import { createFileRoute } from "@tanstack/react-router";
import { validateSkillsSearch } from "@/lib/route-search";

export const Route = createFileRoute("/_app/agents/$agentId/skills/")({
  validateSearch: validateSkillsSearch,
});
