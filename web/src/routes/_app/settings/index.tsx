import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app/settings/")({
  beforeLoad: ({ context: { queryClient } }) => {
    const me = queryClient.getQueryData(meQueryOptions.queryKey);
    throw redirect({ to: me?.is_admin ? "/settings/providers" : "/settings/agents" });
  },
  component: () => null,
});
