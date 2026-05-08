import { Outlet } from "@tanstack/react-router";
import { SiteHeader } from "@/components/SiteHeader";

export function AppLayout() {
  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col">
      <SiteHeader variant="dashboard" />
      <main className="flex-1 w-full overflow-hidden">
        <Outlet />
      </main>
    </div>
  );
}
