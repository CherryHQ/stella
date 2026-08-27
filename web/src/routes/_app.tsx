import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app")({
  loader: async ({ context: { queryClient } }) => {
    try {
      const me = await queryClient.ensureQueryData(meQueryOptions);
      return me;
    } catch (e) {
      // SAFETY: a redirect-throw carries isRedirect; rethrown so TanStack Router handles it.
      if ((e as any)?.isRedirect) throw e;
      throw redirect({ to: "/login" });
    }
  },
});
