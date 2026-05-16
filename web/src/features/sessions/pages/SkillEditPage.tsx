import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { SkillPanel } from "@/features/sessions/panels/SkillPanel";

export function SkillEditPage() {
  const { agentId, skillId } = useParams({ from: "/_app/agents/$agentId/skills/$skillId" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: skills = [] } = useQuery(agentSkillsOptions(agentId));
  const scope = skills.find((s) => s.id === skillId)?.scope;

  return (
    <SkillPanel
      key={skillId}
      skillId={skillId}
      scope={scope}
      agentId={agentId}
      onSaved={() => {
        void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
      }}
      onDeleted={() => {
        void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
        void navigate({ to: "/agents/$agentId/skills", params: { agentId } });
      }}
    />
  );
}
