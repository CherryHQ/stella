import { createLazyFileRoute } from "@tanstack/react-router";
import { ProvidersPage } from "@/features/providers/ProvidersPage";

export const Route = createLazyFileRoute("/_app/admin/ai/providers")({
  component: ProvidersPage,
});
