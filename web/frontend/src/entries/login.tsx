import { createRoot } from "react-dom/client";
import "../globals.css";
import { LoginPage } from "@/components/login/LoginPage";

const root = document.getElementById("login-root");
if (root) {
  createRoot(root).render(<LoginPage />);
}
