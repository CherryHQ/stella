import { createFileRoute } from "@tanstack/react-router";

interface SessionSearch {
  draft?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/sessions/$sessionId")({
  validateSearch: (search: Record<string, unknown>): SessionSearch => ({
    draft: typeof search.draft === "string" ? search.draft : undefined,
  }),
});
