import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { GlobalSearchDialog } from "@/components/GlobalSearchDialog";

const GlobalSearchContext = createContext<(() => void) | null>(null);

const noop = () => {};

/** Opens the ⌘K search dialog. Available anywhere under the app layout. */
export function useGlobalSearch(): () => void {
  return useContext(GlobalSearchContext) ?? noop;
}

/**
 * Owns the one search dialog and its ⌘K shortcut.
 *
 * It lives at app level rather than next to its trigger: the trigger sits in the
 * sidebar, which is unmounted on mobile and hidden when collapsed, and the
 * shortcut has to work on every route regardless.
 */
export function GlobalSearchProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const openSearch = useCallback(() => setOpen(true), []);

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key.toLowerCase() === "k" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        setOpen((current) => !current);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  return (
    <GlobalSearchContext.Provider value={openSearch}>
      {children}
      <GlobalSearchDialog open={open} onOpenChange={setOpen} />
    </GlobalSearchContext.Provider>
  );
}
