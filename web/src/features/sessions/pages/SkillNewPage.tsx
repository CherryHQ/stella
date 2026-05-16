import { useNavigate, useParams } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { SkillPanel } from "@/features/sessions/panels/SkillPanel";

export function SkillNewPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/skills/new" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  return (
    <SkillPanel
      skillId={null}
      agentId={agentId}
      onSaved={() => {
        void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
        void navigate({ to: "/agents/$agentId/skills", params: { agentId } });
      }}
      onDeleted={() => {
        void queryClient.invalidateQueries({ queryKey: ["agent-skills", agentId] });
        void navigate({ to: "/agents/$agentId/skills", params: { agentId } });
      }}
    />
  );
}
