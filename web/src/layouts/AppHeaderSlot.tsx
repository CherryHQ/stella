import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

interface AppHeaderSlotContextValue {
  node: HTMLElement | null;
  setNode: (node: HTMLElement | null) => void;
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
 */
export function AppHeaderSlotProvider({ children }: { children: ReactNode }) {
  const [node, setNode] = useState<HTMLElement | null>(null);
  const value = useMemo(() => ({ node, setNode }), [node]);
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
