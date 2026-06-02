import { useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { agentSkillsOptions } from "@/lib/queries/agents";
import { SkillPanel } from "@/features/sessions/panels/SkillPanel";
import { useAppShell } from "@/layouts/AppShell";

export function SkillEditPage() {
  const { agentId, scope, skillId } = useParams({
    from: "/_app/agents/$agentId/skills/$scope/$skillId",
  });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { setHeaderTitle, setHeaderActions } = useAppShell();
  useQuery(agentSkillsOptions(agentId));

  useEffect(() => {
    setHeaderTitle(
      <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">{skillId}</h1>,
    );
    setHeaderActions(null);
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [setHeaderActions, setHeaderTitle, skillId]);

  return (
    <div className="flex h-full min-w-0 max-w-full overflow-hidden">
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
    </div>
  );
}
