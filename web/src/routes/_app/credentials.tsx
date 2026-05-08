import { createFileRoute } from "@tanstack/react-router";
import { CredentialsPage } from "@/features/credentials/CredentialsPage";

export const Route = createFileRoute("/_app/credentials")({
  component: CredentialsPage,
});
