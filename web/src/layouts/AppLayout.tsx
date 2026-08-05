import { Outlet } from "@tanstack/react-router";
import { AppUpdateNotice } from "@/components/AppUpdateNotice";
import { GlobalSearchProvider } from "@/components/GlobalSearch";

export function AppLayout() {
  return (
    // The search dialog and its ⌘K shortcut sit above the routes: its trigger
    // lives in the sidebar, which is collapsible and sheet-based on mobile.
    <GlobalSearchProvider>
      <div className="flex h-svh flex-col overflow-hidden bg-background text-foreground">
        <main className="flex min-h-0 flex-1 flex-col">
          <Outlet />
        </main>
        <AppUpdateNotice />
      </div>
    </GlobalSearchProvider>
  );
}
