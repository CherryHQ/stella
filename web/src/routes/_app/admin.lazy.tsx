import { createLazyFileRoute } from "@tanstack/react-router";
import { AdminLayout } from "@/features/settings/AdminLayout";

export const Route = createLazyFileRoute("/_app/admin")({
  component: AdminLayout,
});
