import { createLazyFileRoute } from "@tanstack/react-router";
import { AccountPage } from "@/features/account/AccountPage";

export const Route = createLazyFileRoute("/_app/settings/account")({
  component: AccountPage,
});
