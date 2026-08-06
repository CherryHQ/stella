import { createLazyFileRoute } from "@tanstack/react-router";
import { ProvisioningTokensPage } from "@/features/provisioning/ProvisioningTokensPage";

export const Route = createLazyFileRoute("/_app/settings/provisioning")({
  component: ProvisioningTokensPage,
});
