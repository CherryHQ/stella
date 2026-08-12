import { createLazyFileRoute } from "@tanstack/react-router";
import { AboutPage } from "@/features/about/AboutPage";

export const Route = createLazyFileRoute("/_app/admin/overview")({
  component: AboutPage,
});
