import { createLazyFileRoute } from "@tanstack/react-router";
import { SettingsKnowledgePage } from "@/features/knowledge/KnowledgeFilesPage";

export const Route = createLazyFileRoute("/_app/settings/knowledge")({
  component: SettingsKnowledgePage,
});
