import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";
import { authErrorStatus } from "@/lib/auth-error";

export const Route = createFileRoute("/login")({
  beforeLoad: async ({ context: { queryClient } }) => {
    try {
      const me = await queryClient.ensureQueryData(meQueryOptions);
      if (me) throw redirect({ to: "/agents" });
    } catch (e) {
      if ((e as any)?.isRedirect) throw e;
      const status = authErrorStatus(e);
      if (status === 401 || status === 403 || status == null) return;
      throw e;
    }
  },
});
