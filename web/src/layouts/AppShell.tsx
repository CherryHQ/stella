import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import {
  SidebarProvider,
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarInset,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { Separator } from "@/components/ui/separator";
import { AppChromeFooter, AppChromeHeader } from "@/components/AppSidebarChrome";

/** The sidebar column's width, owned here rather than by CossUI's default. */
const SIDEBAR_COLUMN_WIDTH = "16rem";
// SAFETY: the provider reads only the --sidebar-width var; cast for TS CSSProperties.
const SIDEBAR_COLUMN_STYLE = { "--sidebar-width": SIDEBAR_COLUMN_WIDTH } as CSSProperties;

interface AppShellContextType {
  setHeaderTitle: (title: ReactNode) => void;
  setHeaderActions: (actions: ReactNode) => void;
  /**
   * The page's right-panel toggle, if it has one. Kept apart from
   * `setHeaderActions` because it is a layout control, not a page action: it
   * sits at the header's outer edge, mirroring the sidebar trigger.
   */
  setHeaderPanelToggle: (toggle: ReactNode) => void;
}

const AppShellContext = createContext<AppShellContextType | null>(null);

export function useAppShell(): AppShellContextType {
  const context = useContext(AppShellContext);
  if (!context) {
    return {
      setHeaderTitle: () => {},
      setHeaderActions: () => {},
      setHeaderPanelToggle: () => {},
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
 * The app's frame: a full-height sidebar and the content column beside it.
 *
 * The sidebar owns the global chrome — app switcher, search and inbox in its
 * header, the signed-in account pinned to its footer — so those controls follow
 * the sidebar on mobile (where it is a sheet) instead of needing a bar of their
 * own. Every app mounts through this shell, so every app inherits them.
 *
 * The content column owns exactly one slim header: the sidebar trigger at its
 * outer edge (so a collapsed sidebar is always reopenable), the breadcrumb, the
 * page's actions, and — when the page has a right panel — its toggle at the far
 * edge.
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
  const [panelToggle, setPanelToggle] = useState<ReactNode>(null);

  const setHeaderTitle = useCallback((t: ReactNode) => setDynamicTitle(t), []);
  const setHeaderActions = useCallback((a: ReactNode) => setDynamicActions(a), []);
  const setHeaderPanelToggle = useCallback((p: ReactNode) => setPanelToggle(p), []);
  const shellContext = useMemo(
    () => ({ setHeaderTitle, setHeaderActions, setHeaderPanelToggle }),
    [setHeaderTitle, setHeaderActions, setHeaderPanelToggle],
  );

  // The static `title` (breadcrumb spine) always renders; a page's dynamic
  // title appends as the trailing crumb instead of replacing it — replacing is
  // what made the profile entry points vanish once a session mounted. Static
  // headerActions (the "⋯" menu) likewise survive page-level actions.
  const showTailSeparator = title != null && dynamicTitle != null;

  return (
    <AppShellContext.Provider value={shellContext}>
      <SidebarProvider
        defaultOpen={defaultSidebarOpen}
        className="min-h-0 flex-1"
        style={SIDEBAR_COLUMN_STYLE}
      >
        <Sidebar side="left" collapsible="offcanvas">
          {/* shrink-0: SidebarContent's scroll area is sized at 100% of the
              column, so without this the header and footer would give up a few
              pixels each to it instead of the scroller absorbing the overflow. */}
          <SidebarHeader className="shrink-0">
            <AppChromeHeader />
          </SidebarHeader>
          <SidebarContent>{sidebar}</SidebarContent>
          {/* The sidebar's only rule: it fences off the pinned footer (design
              says lines are scarce — hierarchy elsewhere comes from type). */}
          <SidebarFooter className="shrink-0 border-t">
            <AppChromeFooter />
          </SidebarFooter>
        </Sidebar>
        <SidebarInset className="min-w-0 overflow-hidden">
          <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border px-2">
            <SidebarTrigger className="shrink-0" />
            <div className="flex min-w-0 flex-1 items-center gap-2">
              {title != null && <div className="flex min-w-0 shrink-0 items-center">{title}</div>}
              {showTailSeparator && (
                <span aria-hidden className="shrink-0 text-muted-foreground">
                  /
                </span>
              )}
              {dynamicTitle != null && (
                <div className="min-w-0 flex-1 truncate">{dynamicTitle}</div>
              )}
            </div>
            {(dynamicActions || headerActions) && (
              <div className="flex shrink-0 items-center gap-1">
                {dynamicActions}
                {headerActions}
              </div>
            )}
            {panelToggle && (
              <>
                <Separator orientation="vertical" className="h-4" />
                <div className="flex shrink-0 items-center">{panelToggle}</div>
              </>
            )}
          </header>
          <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">{children}</div>
        </SidebarInset>
      </SidebarProvider>
    </AppShellContext.Provider>
  );
}
