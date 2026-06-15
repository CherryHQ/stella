import { createLazyFileRoute } from "@tanstack/react-router";
import { SkillsPage } from "@/features/skills/SkillsPage";

export const Route = createLazyFileRoute("/_app/settings/skills")({
  component: SkillsPage,
});
