import { createRoot } from "react-dom/client";
import "../globals.css";
import { UsersPage } from "@/components/users/UsersPage";

const root = document.getElementById("users-root");
if (root) {
  createRoot(root).render(<UsersPage />);
}
