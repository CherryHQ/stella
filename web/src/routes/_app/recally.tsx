import { createFileRoute } from "@tanstack/react-router";

interface RecallySearch {
  // Deep-link to a specific article (e.g. from an agent-created reference card).
  article?: string;
}

export const Route = createFileRoute("/_app/recally")({
  validateSearch: (search: Record<string, unknown>): RecallySearch => ({
    article: typeof search.article === "string" ? search.article : undefined,
  }),
});
