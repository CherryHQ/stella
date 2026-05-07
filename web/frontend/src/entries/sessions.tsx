import { createRoot } from "react-dom/client";
import "../globals.css";
import { SessionsPage } from "@/components/sessions/SessionsPage";

const root = document.getElementById("sessions-root");
if (root) {
  createRoot(root).render(<SessionsPage />);
}
