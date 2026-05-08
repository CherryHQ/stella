import { createFileRoute, redirect } from "@tanstack/react-router";
import { LoginPage } from "@/features/login/LoginPage";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/login")({
  beforeLoad: async ({ context: { queryClient } }) => {
    try {
      await queryClient.ensureQueryData(meQueryOptions);
      throw redirect({ to: "/sessions" as any });
    } catch (e) {
      if ((e as any)?.isRedirect) throw e;
      // not authenticated — render login
    }
  },
  component: LoginPage,
});
