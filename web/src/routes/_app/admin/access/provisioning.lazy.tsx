import { createLazyFileRoute } from "@tanstack/react-router";
import { ProvisioningTokensPage } from "@/features/provisioning/ProvisioningTokensPage";

export const Route = createLazyFileRoute("/_app/admin/access/provisioning")({
  component: ProvisioningTokensPage,
});
