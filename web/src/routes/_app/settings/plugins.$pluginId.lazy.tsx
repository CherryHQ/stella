import { createLazyFileRoute } from "@tanstack/react-router";
import { PluginsPage } from "@/features/plugins/PluginsPage";

export const Route = createLazyFileRoute("/_app/settings/plugins/$pluginId")({
  component: PluginsPage,
});
