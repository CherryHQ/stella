import { createLazyFileRoute } from "@tanstack/react-router";
import { WebhooksPage } from "@/features/webhooks/WebhooksPage";
export const Route = createLazyFileRoute("/_app/settings/webhooks")({ component: WebhooksPage });
