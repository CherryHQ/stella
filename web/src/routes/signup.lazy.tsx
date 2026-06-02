import { createLazyFileRoute } from "@tanstack/react-router";
import { SignupPage } from "@/features/signup/SignupPage";

export const Route = createLazyFileRoute("/signup")({
  component: SignupPage,
});
