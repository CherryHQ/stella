import { createLazyFileRoute } from "@tanstack/react-router";
import { AdminPluginsPage } from "@/features/plugins/PluginsPage";

export const Route = createLazyFileRoute("/_app/admin/integrations/plugins/$pluginId")({
  component: AdminPluginsPage,
});
