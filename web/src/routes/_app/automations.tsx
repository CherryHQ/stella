import { createFileRoute } from "@tanstack/react-router";
import { AutomationsPage } from "@/features/automations/AutomationsPage";

export const Route = createFileRoute("/_app/automations")({
  component: AutomationsPage,
});
