import { createLazyFileRoute } from "@tanstack/react-router";
import { LoginPage } from "@/features/login/LoginPage";

export const Route = createLazyFileRoute("/login")({
  component: LoginPage,
});
