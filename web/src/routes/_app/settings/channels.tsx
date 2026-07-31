import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app/settings/channels")({
  beforeLoad: async ({ context: { queryClient } }) => {
    const me = await queryClient.ensureQueryData(meQueryOptions);
    if (!me?.is_admin) throw redirect({ to: "/settings/webhooks" });
  },
});
