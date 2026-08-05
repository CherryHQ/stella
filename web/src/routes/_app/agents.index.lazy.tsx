import { createLazyFileRoute } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";
import { HomePage } from "@/features/home/HomePage";
import { ConversationSidebar } from "@/features/sessions/ConversationSidebar";
import { AppShell } from "@/layouts/AppShell";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbList,
  BreadcrumbPage,
} from "@/components/ui/breadcrumb";

export const Route = createLazyFileRoute("/_app/agents/")({
  component: HomeRoute,
});

function HomeRoute() {
  const { t } = useI18n();
  return (
    <AppShell
      sidebar={<ConversationSidebar />}
      title={
        <Breadcrumb className="min-w-0">
          <BreadcrumbList>
            <BreadcrumbItem>
              <BreadcrumbPage className="truncate font-medium">{t("nav.agents")}</BreadcrumbPage>
            </BreadcrumbItem>
          </BreadcrumbList>
        </Breadcrumb>
      }
    >
      <HomePage />
    </AppShell>
  );
}
