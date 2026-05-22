import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { SkillPanel } from "@/features/sessions/panels/SkillPanel";

export function SkillEditPage() {
  const { agentId, scope, skillId } = useParams({
    from: "/_app/agents/$agentId/skills/$scope/$skillId",
  });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  useQuery(agentSkillsOptions(agentId));

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
