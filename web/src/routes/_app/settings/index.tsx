import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app/settings/")({
  beforeLoad: async ({ context: { queryClient } }) => {
    const me = await queryClient.ensureQueryData(meQueryOptions);
    throw redirect({ to: me?.is_admin ? "/settings/providers" : "/settings/agents" });
  },
  component: () => null,
});
