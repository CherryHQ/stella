import { createFileRoute } from "@tanstack/react-router";
import { SessionsPage } from "@/features/sessions/SessionsPage";

export const Route = createFileRoute("/_app/sessions/$")({
  component: SessionsPage,
});
