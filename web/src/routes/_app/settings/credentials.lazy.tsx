import { createLazyFileRoute } from "@tanstack/react-router";
import { PersonalCredentialsPage } from "@/features/credentials/CredentialsPage";

export const Route = createLazyFileRoute("/_app/settings/credentials")({
  component: PersonalCredentialsPage,
});
