import { Outlet } from "@tanstack/react-router";
import { SiteHeader } from "@/components/SiteHeader";

export function AppLayout() {
  return (
    <div className="relative isolate flex h-svh flex-col overflow-hidden bg-background text-foreground">
      <SiteHeader />
      <main className="min-h-0 flex-1 w-full overflow-hidden">
        <Outlet />
      </main>
    </div>
  );
}
