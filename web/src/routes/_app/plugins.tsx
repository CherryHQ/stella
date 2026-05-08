import { createFileRoute, redirect } from "@tanstack/react-router";
import { PluginsPage } from "@/features/plugins/PluginsPage";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app/plugins")({
  beforeLoad: async ({ context: { queryClient } }) => {
    const me = queryClient.getQueryData(meQueryOptions.queryKey);
    if (!me?.is_admin) throw redirect({ to: "/agents" });
  },
  component: PluginsPage,
});
