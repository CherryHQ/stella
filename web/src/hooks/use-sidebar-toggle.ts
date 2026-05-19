import { createContext, useContext } from "react";

interface SidebarToggleContext {
  sidebarOpen: boolean;
  toggleSidebar: () => void;
}

export const SidebarToggleContext = createContext<SidebarToggleContext>({
  sidebarOpen: true,
  toggleSidebar: () => {},
});

export function useSidebarToggle() {
  return useContext(SidebarToggleContext);
}
