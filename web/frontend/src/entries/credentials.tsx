import { createRoot } from "react-dom/client";
import "../globals.css";
import { CredentialsPage } from "@/components/credentials/CredentialsPage";

const root = document.getElementById("credentials-root");
if (root) {
  createRoot(root).render(<CredentialsPage />);
}
