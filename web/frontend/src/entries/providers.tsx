import { createRoot } from "react-dom/client";
import "../globals.css";
import { ProvidersPage } from "@/components/providers/ProvidersPage";

const root = document.getElementById("providers-root");
if (root) {
  createRoot(root).render(<ProvidersPage />);
}
