import { Outlet } from "@tanstack/react-router";
import { AppUpdateNotice } from "@/components/AppUpdateNotice";
import { GlobalTopBar } from "@/components/GlobalTopBar";
import { AppHeaderSlotProvider } from "@/layouts/AppHeaderSlot";

export function AppLayout() {
  return (
    <AppHeaderSlotProvider>
      <div className="flex h-svh flex-col overflow-hidden bg-background text-foreground">
        <GlobalTopBar />
        <main className="flex min-h-0 flex-1 flex-col">
          <Outlet />
        </main>
        <AppUpdateNotice />
      </div>
    </AppHeaderSlotProvider>
  );
}
