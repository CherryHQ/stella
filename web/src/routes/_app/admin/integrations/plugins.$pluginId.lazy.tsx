import { createLazyFileRoute } from "@tanstack/react-router";
import { PluginsPage } from "@/features/plugins/PluginsPage";

export const Route = createLazyFileRoute("/_app/admin/integrations/plugins/$pluginId")({
  component: PluginsPage,
});
