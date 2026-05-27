import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";
import { OnboardingPage } from "@/features/onboarding/OnboardingPage";

export const Route = createFileRoute("/onboarding")({
  beforeLoad: async ({ context: { queryClient } }) => {
    try {
      const me = await queryClient.ensureQueryData(meQueryOptions);
      if (me.org_id) {
        throw redirect({ to: "/sessions" as any });
      }
    } catch (e) {
      if ((e as any)?.isRedirect) throw e;
      throw redirect({ to: "/login" });
    }
  },
  component: OnboardingPage,
});
