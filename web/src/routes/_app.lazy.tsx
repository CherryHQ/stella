import { createLazyFileRoute } from "@tanstack/react-router";
import { AppLayout } from "@/layouts/AppLayout";

export const Route = createLazyFileRoute("/_app")({
  component: AppLayout,
});
