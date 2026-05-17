import { createLazyFileRoute } from "@tanstack/react-router";
import { ProvidersPage } from "@/features/providers/ProvidersPage";

export const Route = createLazyFileRoute("/_app/settings/providers")({
  component: ProvidersPage,
});
