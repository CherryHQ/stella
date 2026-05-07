import { createRoot } from "react-dom/client";
import "../globals.css";
import { AgentsPage } from "@/components/agents/AgentsPage";

const root = document.getElementById("agents-root");
if (root) {
  createRoot(root).render(<AgentsPage />);
}
