import { createFileRoute } from "@tanstack/react-router";

// No admin guard: the page lists the channels the caller may manage, which is
// every channel for an admin and their own agents' channels for anyone else.
export const Route = createFileRoute("/_app/settings/channels")({});
