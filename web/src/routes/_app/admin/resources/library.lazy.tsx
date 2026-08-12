import { createLazyFileRoute } from "@tanstack/react-router";
import { GlobalLibraryPage } from "@/features/library/LibraryFilesPage";

export const Route = createLazyFileRoute("/_app/admin/resources/library")({
  component: GlobalLibraryPage,
});
