import { Outlet } from "@tanstack/react-router";

export function AppLayout() {
  return (
    <div className="flex h-svh flex-col overflow-hidden bg-background text-foreground">
      <main className="min-h-0 flex-1 overflow-hidden">
        <Outlet />
      </main>
    </div>
  );
}
