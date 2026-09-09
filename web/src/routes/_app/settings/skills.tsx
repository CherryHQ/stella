import { createFileRoute } from "@tanstack/react-router";
import { isString, type RouteSearchInput } from "@/lib/route-search";

export interface SkillSettingsSearch {
  skill_id?: string;
}

export const Route = createFileRoute("/_app/settings/skills")({
  validateSearch: (search: RouteSearchInput): SkillSettingsSearch => ({
    skill_id: isString(search.skill_id) && search.skill_id ? search.skill_id : undefined,
  }),
});
