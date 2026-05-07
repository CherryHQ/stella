import { createRoot } from "react-dom/client";
import "../globals.css";
import { PluginsPage } from "@/components/plugins/PluginsPage";

const root = document.getElementById("plugins-root");
if (root) {
  createRoot(root).render(<PluginsPage />);
}
