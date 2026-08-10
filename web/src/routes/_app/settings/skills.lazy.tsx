import { createLazyFileRoute } from "@tanstack/react-router";
import { PersonalSkillsPage } from "@/features/skills/SkillsPage";

export const Route = createLazyFileRoute("/_app/settings/skills")({
  component: PersonalSkillsPage,
});
