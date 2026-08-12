import { createLazyFileRoute } from "@tanstack/react-router";
import { GlobalSkillsPage } from "@/features/skills/SkillsPage";

export const Route = createLazyFileRoute("/_app/admin/resources/skills")({
  component: GlobalSkillsPage,
});
