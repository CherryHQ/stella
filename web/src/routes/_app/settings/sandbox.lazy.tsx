import { createLazyFileRoute } from "@tanstack/react-router";
import { SandboxPage } from "@/features/sandbox/SandboxPage";

export const Route = createLazyFileRoute("/_app/settings/sandbox")({
  component: SandboxPage,
});
