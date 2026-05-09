import { Outlet } from "@tanstack/react-router";
import { SiteHeader } from "@/components/SiteHeader";
import { I18nProvider } from "@/lib/i18n";

export function AppLayout() {
  return (
    <I18nProvider>
      <div className="min-h-screen bg-background text-foreground flex flex-col">
        <SiteHeader />
        <main className="flex-1 w-full overflow-hidden">
          <Outlet />
        </main>
      </div>
    </I18nProvider>
  );
}
