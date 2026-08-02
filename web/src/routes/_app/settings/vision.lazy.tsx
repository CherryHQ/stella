import { createLazyFileRoute } from "@tanstack/react-router";
import { VisionPage } from "@/features/vision/VisionPage";

export const Route = createLazyFileRoute("/_app/settings/vision")({
  component: VisionPage,
});
