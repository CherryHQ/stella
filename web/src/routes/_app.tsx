import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app")({
  loader: async ({ context: { queryClient } }) => {
    try {
      return await queryClient.ensureQueryData(meQueryOptions);
    } catch {
      throw redirect({ to: "/login" });
    }
  },
});
