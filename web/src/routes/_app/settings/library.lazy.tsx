import { createLazyFileRoute } from "@tanstack/react-router";
import { SettingsLibraryPage } from "@/features/library/LibraryFilesPage";

export const Route = createLazyFileRoute("/_app/settings/library")({
  component: SettingsLibraryPage,
});
