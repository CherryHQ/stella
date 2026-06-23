import { createLazyFileRoute } from "@tanstack/react-router";
import { EmbeddingPage } from "@/features/embeddings/EmbeddingPage";

export const Route = createLazyFileRoute("/_app/settings/embedding")({
  component: EmbeddingPage,
});
