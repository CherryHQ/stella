import { createFileRoute } from "@tanstack/react-router";
import { ChannelsPage } from "@/components/channels/ChannelsPage";

export const Route = createFileRoute("/_app/channels")({
  component: ChannelsPage,
});
