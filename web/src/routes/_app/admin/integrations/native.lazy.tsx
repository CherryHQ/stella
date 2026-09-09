import { createLazyFileRoute } from "@tanstack/react-router";
import { NativePluginsPage } from "@/features/plugins/NativePluginsPage";

export const Route = createLazyFileRoute("/_app/admin/integrations/native")({
  component: NativePluginsPage,
});
