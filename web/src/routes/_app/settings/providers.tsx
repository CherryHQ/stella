import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app/settings/providers")({
  beforeLoad: async ({ context: { queryClient } }) => {
    const me = queryClient.getQueryData(meQueryOptions.queryKey);
    if (!me?.is_admin) throw redirect({ to: "/settings/agents" });
  },
});
