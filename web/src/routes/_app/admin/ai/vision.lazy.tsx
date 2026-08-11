import { createLazyFileRoute } from "@tanstack/react-router";
import { VisionPage } from "@/features/vision/VisionPage";

export const Route = createLazyFileRoute("/_app/admin/ai/vision")({
  component: VisionPage,
});
