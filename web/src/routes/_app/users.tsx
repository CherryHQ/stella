import { createFileRoute, redirect } from "@tanstack/react-router";
import { UsersPage } from "@/components/users/UsersPage";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app/users")({
  beforeLoad: async ({ context: { queryClient } }) => {
    const me = queryClient.getQueryData(meQueryOptions.queryKey);
    if (!me?.is_admin) throw redirect({ to: "/agents" });
  },
  component: UsersPage,
});
