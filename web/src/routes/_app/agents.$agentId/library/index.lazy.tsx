import { createLazyFileRoute } from "@tanstack/react-router";
import { AgentLibraryPage } from "@/features/library/LibraryFilesPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/library/")({
  component: AgentLibraryPage,
});
