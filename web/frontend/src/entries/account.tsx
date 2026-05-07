import { createRoot } from "react-dom/client";
import "../globals.css";
import { AccountPage } from "@/components/account/AccountPage";

const root = document.getElementById("account-root");
if (root) {
  createRoot(root).render(<AccountPage />);
}
