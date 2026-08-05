import { createLazyFileRoute } from "@tanstack/react-router";
import { ThreadsPage } from "@/features/sessions/pages/ThreadsPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/threads")({
  component: ThreadsPage,
});
