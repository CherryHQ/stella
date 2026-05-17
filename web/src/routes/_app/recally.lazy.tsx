import { createLazyFileRoute } from "@tanstack/react-router";
import { RecallyPage } from "@/features/recally/RecallyPage";

export const Route = createLazyFileRoute("/_app/recally")({
  component: RecallyPage,
});
