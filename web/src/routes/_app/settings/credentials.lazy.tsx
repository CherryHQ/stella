import { createLazyFileRoute } from "@tanstack/react-router";
import { CredentialsPage } from "@/features/credentials/CredentialsPage";

export const Route = createLazyFileRoute("/_app/settings/credentials")({
  component: CredentialsPage,
});
