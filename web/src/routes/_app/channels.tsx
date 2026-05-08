import { createFileRoute } from "@tanstack/react-router";
import { ChannelsPage } from "@/features/channels/ChannelsPage";

export const Route = createFileRoute("/_app/channels")({
  component: ChannelsPage,
});
