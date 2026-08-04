import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

interface AppHeaderSlotContextValue {
  node: HTMLElement | null;
  setNode: (node: HTMLElement | null) => void;
  /**
   * CSS width of the sidebar column that currently exists below the bar, or
   * `null` when there is none (collapsed sidebar, mobile, shell-less route).
   */
  leadWidth: string | null;
  setLeadWidth: (width: string | null) => void;
}

const AppHeaderSlotContext = createContext<AppHeaderSlotContextValue | null>(null);

/**
 * Shares the single top bar's contextual region between the bar (which owns the
 * DOM node) and whichever shell is mounted below it (which fills it).
 *
 * A portal rather than lifted state: the contextual content is a sidebar trigger
 * plus page-supplied nodes, and both need the shell's React context —
 * `SidebarTrigger` is inert outside `SidebarProvider`. A portal keeps the React
 * tree (and therefore the context) intact while moving only the DOM.
 *
 * The same channel carries the sidebar column's width upward, so the bar can
 * split itself along the same vertical seam as the panes below it.
 */
export function AppHeaderSlotProvider({ children }: { children: ReactNode }) {
  const [node, setNode] = useState<HTMLElement | null>(null);
  const [leadWidth, setLeadWidth] = useState<string | null>(null);
  const value = useMemo(() => ({ node, setNode, leadWidth, setLeadWidth }), [node, leadWidth]);
  return <AppHeaderSlotContext.Provider value={value}>{children}</AppHeaderSlotContext.Provider>;
}

/** Rendered once, by the top bar: the region shells render into. */
export function AppHeaderSlotTarget({ className }: { className?: string }) {
  const context = useContext(AppHeaderSlotContext);
  return <div ref={context?.setNode} className={className} />;
}

/** Rendered by a shell: its children appear inside the top bar's slot. */
export function AppHeaderSlotContent({ children }: { children: ReactNode }) {
  const node = useContext(AppHeaderSlotContext)?.node;
  if (!node) return null;
  return createPortal(children, node);
}

/**
 * Rendered by a shell from inside its `SidebarProvider`: publishes the width of
 * the sidebar column so the bar's left segment can end exactly where the
 * sidebar/content seam does. `null` while there is no column (collapsed, mobile),
 * which lets the left segment fall back to its content width.
 */
export function AppHeaderLeadColumn({ width }: { width: string | null }) {
  const setLeadWidth = useContext(AppHeaderSlotContext)?.setLeadWidth;
  useEffect(() => {
    if (!setLeadWidth) return;
    setLeadWidth(width);
    // Shell-less routes must not inherit the last shell's column.
    return () => setLeadWidth(null);
  }, [setLeadWidth, width]);
  return null;
}

/** Read by the top bar: the column width its left segment must track. */
export function useAppHeaderLeadWidth(): string | null {
  return useContext(AppHeaderSlotContext)?.leadWidth ?? null;
}
