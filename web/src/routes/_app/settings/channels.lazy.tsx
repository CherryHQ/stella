import { createLazyFileRoute } from "@tanstack/react-router";
import { ChannelsPage } from "@/features/channels/ChannelsPage";

export const Route = createLazyFileRoute("/_app/settings/channels")({
  component: ChannelsPage,
});
