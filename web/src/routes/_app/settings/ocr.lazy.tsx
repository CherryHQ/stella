import { createLazyFileRoute } from "@tanstack/react-router";
import { OCRPage } from "@/features/ocr/OCRPage";

export const Route = createLazyFileRoute("/_app/settings/ocr")({
  component: OCRPage,
});
