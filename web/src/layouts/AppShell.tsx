import {
  createContext,
  useCallback,
  useContext,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import {
  SidebarProvider,
  Sidebar,
  SidebarContent,
  SidebarInset,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";
import { Separator } from "@/components/ui/separator";
import { AppHeaderLeadColumn, AppHeaderSlotContent } from "@/layouts/AppHeaderSlot";

/**
 * The sidebar column's width, owned here rather than by CossUI's default so the
 * top bar's left segment and the sidebar are driven by one number.
 */
const SIDEBAR_COLUMN_WIDTH = "16rem";

/** Publishes the live sidebar geometry to the bar above the provider. */
function SidebarColumnBridge() {
  const { open, isMobile } = useSidebar();
  // Mobile's sidebar is an overlay sheet, so it owns no column in the layout.
  return <AppHeaderLeadColumn width={!isMobile && open ? SIDEBAR_COLUMN_WIDTH : null} />;
}

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
 * The per-app sidebar and the content pane under the single top bar.
 *
 * This shell has no header row of its own: its contextual header content
 * (sidebar trigger, breadcrumb, page actions) is portalled into the top bar's
 * slot, so the app shows one h-14 bar instead of two stacked ones. The same
 * slot carries the sidebar's live width up to the bar (`SidebarColumnBridge`),
 * which is what keeps the bar's segments aligned with these two columns.
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

  // The static `title` (breadcrumb spine) always renders; a page's dynamic
  // title appends as the trailing crumb instead of replacing it — replacing is
  // what made the profile entry points vanish once a session mounted. Static
  // headerActions (the "⋯" menu) likewise survive page-level actions.
  const showTailSeparator = title != null && dynamicTitle != null;

  return (
    <AppShellContext.Provider value={{ setHeaderTitle, setHeaderActions }}>
      <SidebarProvider
        defaultOpen={defaultSidebarOpen}
        className="min-h-0 flex-1"
        style={{ "--sidebar-width": SIDEBAR_COLUMN_WIDTH } as CSSProperties}
      >
        <SidebarColumnBridge />
        {/* inset-y-auto/h-auto undo CossUI's viewport-height fixed placement so
            the fixed sidebar starts below the h-14 global bar instead of under it. */}
        <Sidebar
          side="left"
          collapsible="offcanvas"
          className="inset-y-auto bottom-0 top-14 h-auto"
        >
          <SidebarContent>{sidebar}</SidebarContent>
        </Sidebar>
        {/* This is the content column's own header: the trigger sits at the
            column's left edge, the breadcrumb next to it, page actions in one
            cluster at the far end, and a rule closing the column off from the
            bar's global controls. */}
        <AppHeaderSlotContent>
          <SidebarTrigger className="shrink-0" />
          <div className="flex min-w-0 flex-1 items-center gap-2">
            {title != null && <div className="flex min-w-0 shrink-0 items-center">{title}</div>}
            {showTailSeparator && (
              <span aria-hidden className="shrink-0 text-muted-foreground">
                /
              </span>
            )}
            {dynamicTitle != null && <div className="min-w-0 flex-1 truncate">{dynamicTitle}</div>}
          </div>
          {(dynamicActions || headerActions) && (
            <div className="flex shrink-0 items-center gap-1">
              {dynamicActions}
              {headerActions}
            </div>
          )}
          <Separator orientation="vertical" className="h-4" />
        </AppHeaderSlotContent>
        <SidebarInset className="min-w-0 overflow-hidden">
          <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">{children}</div>
        </SidebarInset>
      </SidebarProvider>
    </AppShellContext.Provider>
  );
}
