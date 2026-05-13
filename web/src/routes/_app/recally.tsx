import { createFileRoute } from "@tanstack/react-router";
import { RecallyPage } from "@/features/recally/RecallyPage";

export const Route = createFileRoute("/_app/recally")({
  component: RecallyPage,
});
