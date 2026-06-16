import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import {
  SidebarProvider,
  Sidebar,
  SidebarContent,
  SidebarHeader,
  SidebarFooter,
  SidebarInset,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { AppSidebarHeader, AppSidebarFooter } from "@/components/AppSidebar";
import { Separator } from "@/components/ui/separator";

interface AppShellContextType {
  setHeaderTitle: (title: ReactNode) => void;
  setHeaderActions: (actions: ReactNode) => void;
}

const AppShellContext = createContext<AppShellContextType | null>(null);

export function useAppShell(): AppShellContextType {
  const context = useContext(AppShellContext);
  if (!context) {
    return {
      setHeaderTitle: () => {},
      setHeaderActions: () => {},
    };
  }
  return context;
}

interface AppShellProps {
  sidebar: ReactNode;
  children: ReactNode;
  defaultSidebarOpen?: boolean;
  title?: ReactNode;
  headerActions?: ReactNode;
  subnav?: ReactNode;
}

export function AppShell({
  sidebar,
  children,
  defaultSidebarOpen = true,
  title,
  headerActions,
  subnav,
}: AppShellProps) {
  const [dynamicTitle, setDynamicTitle] = useState<ReactNode>(null);
  const [dynamicActions, setDynamicActions] = useState<ReactNode>(null);

  const setHeaderTitle = useCallback((t: ReactNode) => setDynamicTitle(t), []);
  const setHeaderActions = useCallback((a: ReactNode) => setDynamicActions(a), []);

  const displayTitle = dynamicTitle ?? title;
  const displayActions = dynamicActions ?? headerActions;

  return (
    <AppShellContext.Provider value={{ setHeaderTitle, setHeaderActions }}>
      <SidebarProvider defaultOpen={defaultSidebarOpen}>
        <Sidebar side="left" collapsible="offcanvas">
          <SidebarHeader className="p-0">
            <AppSidebarHeader />
          </SidebarHeader>
          <SidebarContent>{sidebar}</SidebarContent>
          <SidebarFooter>
            <AppSidebarFooter />
          </SidebarFooter>
        </Sidebar>
        <SidebarInset className="min-w-0 overflow-hidden">
          <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border bg-card/65 px-4 backdrop-blur-xl">
            <SidebarTrigger className="-ml-1" />
            <Separator orientation="vertical" className="mr-1 h-4" />
            <div
              className={subnav ? "hidden min-w-0 max-w-md shrink-0 sm:block" : "min-w-0 flex-1"}
            >
              {displayTitle}
            </div>
            {subnav ? <div className="min-w-0 flex-1">{subnav}</div> : null}
            {displayActions && <div className="flex shrink-0 items-center">{displayActions}</div>}
          </header>
          <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">{children}</div>
        </SidebarInset>
      </SidebarProvider>
    </AppShellContext.Provider>
  );
}
