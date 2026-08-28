import { createLazyFileRoute } from "@tanstack/react-router";
import { DefaultModelsPage } from "@/features/default-models/DefaultModelsPage";

export const Route = createLazyFileRoute("/_app/admin/ai/models")({
  component: DefaultModelsPage,
});
