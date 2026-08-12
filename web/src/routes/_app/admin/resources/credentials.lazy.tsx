import { createLazyFileRoute } from "@tanstack/react-router";
import { SystemCredentialsPage } from "@/features/credentials/CredentialsPage";

export const Route = createLazyFileRoute("/_app/admin/resources/credentials")({
  component: SystemCredentialsPage,
});
