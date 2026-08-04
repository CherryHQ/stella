import { Outlet } from "@tanstack/react-router";
import { AppUpdateNotice } from "@/components/AppUpdateNotice";
import { GlobalTopBar } from "@/components/GlobalTopBar";

export function AppLayout() {
  return (
    <div className="flex h-svh flex-col overflow-hidden bg-background text-foreground">
      <GlobalTopBar />
      <main className="flex min-h-0 flex-1 flex-col">
        <Outlet />
      </main>
      <AppUpdateNotice />
    </div>
  );
}
