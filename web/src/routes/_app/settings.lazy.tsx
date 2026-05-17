import { createLazyFileRoute } from "@tanstack/react-router";
import { SettingsLayout } from "@/features/settings/SettingsLayout";

export const Route = createLazyFileRoute("/_app/settings")({
  component: SettingsLayout,
});
