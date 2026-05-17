import { createLazyFileRoute } from "@tanstack/react-router";
import { AboutPage } from "@/features/about/AboutPage";

export const Route = createLazyFileRoute("/_app/settings/about")({
  component: AboutPage,
});
