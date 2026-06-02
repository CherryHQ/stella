import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

function authErrorStatus(e: unknown): number | undefined {
  const err = e as any;
  return err?.error?.code ?? err?.code ?? err?.status ?? err?.response?.status;
}

export const Route = createFileRoute("/login")({
  beforeLoad: async ({ context: { queryClient } }) => {
    try {
      const me = await queryClient.ensureQueryData(meQueryOptions);
      if (me) throw redirect({ to: "/sessions" as any });
    } catch (e) {
      if ((e as any)?.isRedirect) throw e;
      const status = authErrorStatus(e);
      if (status === 401 || status === 403) return;
      throw e;
    }
  },
});
