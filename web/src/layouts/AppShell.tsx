import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import {
  SidebarProvider,
  Sidebar,
  SidebarContent,
  SidebarInset,
  SidebarTrigger,
} from "@/components/ui/sidebar";
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
}

/**
 * L1 + L2: the per-app sidebar and the content pane under the global top bar.
 *
 * The desktop sidebar is `position: fixed`, so it has to be told where the top
 * bar ends — hence the h-14 offset here. Everything else (width, collapse,
 * mobile offcanvas) stays CossUI's.
 */
export function AppShell({
  sidebar,
  children,
  defaultSidebarOpen = true,
  title,
  headerActions,
}: AppShellProps) {
  const [dynamicTitle, setDynamicTitle] = useState<ReactNode>(null);
  const [dynamicActions, setDynamicActions] = useState<ReactNode>(null);

  const setHeaderTitle = useCallback((t: ReactNode) => setDynamicTitle(t), []);
  const setHeaderActions = useCallback((a: ReactNode) => setDynamicActions(a), []);

  const displayTitle = dynamicTitle ?? title;
  const displayActions = dynamicActions ?? headerActions;

  return (
    <AppShellContext.Provider value={{ setHeaderTitle, setHeaderActions }}>
      <SidebarProvider defaultOpen={defaultSidebarOpen} className="min-h-0 flex-1">
        {/* inset-y-auto/h-auto undo CossUI's viewport-height fixed placement so
            the fixed sidebar starts below the h-14 global bar instead of under it. */}
        <Sidebar
          side="left"
          collapsible="offcanvas"
          className="inset-y-auto bottom-0 top-14 h-auto"
        >
          <SidebarContent>{sidebar}</SidebarContent>
        </Sidebar>
        <SidebarInset className="min-w-0 overflow-hidden">
          <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border bg-card/65 px-4 backdrop-blur-xl">
            <SidebarTrigger className="-ml-1" />
            <Separator orientation="vertical" className="mr-1 h-4" />
            <div className="min-w-0 flex-1">{displayTitle}</div>
            {displayActions && <div className="flex shrink-0 items-center">{displayActions}</div>}
          </header>
          <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">{children}</div>
        </SidebarInset>
      </SidebarProvider>
    </AppShellContext.Provider>
  );
}
