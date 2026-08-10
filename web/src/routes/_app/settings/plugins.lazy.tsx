import { createLazyFileRoute } from "@tanstack/react-router";
import { PersonalToolsPage } from "@/features/plugins/PluginsPage";

export const Route = createLazyFileRoute("/_app/settings/plugins")({
  component: PersonalToolsPage,
});
