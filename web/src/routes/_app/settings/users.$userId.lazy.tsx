import { createLazyFileRoute } from "@tanstack/react-router";
import { UsersPage } from "@/features/users/UsersPage";

export const Route = createLazyFileRoute("/_app/settings/users/$userId")({
  component: UsersPage,
});
