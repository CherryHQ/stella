import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/login")({
  beforeLoad: async ({ context: { queryClient } }) => {
    try {
      const me = await queryClient.ensureQueryData(meQueryOptions);
      if (me) throw redirect({ to: "/sessions" as any });
    } catch (e) {
      if ((e as any)?.isRedirect) throw e;
      if (e instanceof Response && e.status >= 500) throw e;
      // 401/403 or network error — render login
    }
  },
});
