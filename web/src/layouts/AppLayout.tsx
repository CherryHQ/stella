import { Outlet } from "@tanstack/react-router";
import { SiteHeader } from "@/components/SiteHeader";

export function AppLayout() {
  return (
    <div className="relative isolate flex min-h-svh flex-col bg-background text-foreground">
      <SiteHeader />
      <main className="flex-1 w-full overflow-hidden">
        <Outlet />
      </main>
    </div>
  );
}
