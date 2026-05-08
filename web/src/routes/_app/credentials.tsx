import { createFileRoute } from "@tanstack/react-router";
import { CredentialsPage } from "@/components/credentials/CredentialsPage";

export const Route = createFileRoute("/_app/credentials")({
  component: CredentialsPage,
});
