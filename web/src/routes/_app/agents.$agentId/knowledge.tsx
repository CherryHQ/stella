import { createFileRoute } from "@tanstack/react-router";
import { AgentKnowledgePage } from "@/features/knowledge/KnowledgeFilesPage";

interface AgentKnowledgeSearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/knowledge")({
  validateSearch: (search: Record<string, unknown>): AgentKnowledgeSearch => ({
    q: typeof search.q === "string" && search.q ? search.q.slice(0, 200) : undefined,
  }),
  component: AgentKnowledgePage,
});
