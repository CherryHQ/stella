import { createContext, useContext } from "react";

export interface AgentLayoutContextType {
  sidebarOpen: boolean;
  toggleSidebar: () => void;
  openMobileSidebar: () => void;
}

export const AgentLayoutContext = createContext<AgentLayoutContextType | null>(null);

export function useAgentLayout() {
  const context = useContext(AgentLayoutContext);
  if (!context) {
    return { sidebarOpen: true, toggleSidebar: () => {}, openMobileSidebar: () => {} };
  }
  return context;
}
