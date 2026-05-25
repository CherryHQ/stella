import { createFileRoute } from "@tanstack/react-router";
import { InviteAcceptPage } from "@/features/invites/InviteAcceptPage";

export const Route = createFileRoute("/auth/invite/$token/accept")({
  component: InviteAcceptPage,
});
