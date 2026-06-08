import { useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";

export function SkillEditPage() {
  const { agentId, scope, skillId } = useParams({ strict: false }) as {
    agentId: string;
    scope?: string;
    skillId?: string;
  };
  const navigate = useNavigate();

  useEffect(() => {
    if (agentId) {
      void navigate({
        to: "/agents/$agentId/skills",
        params: { agentId },
        search: { expand: skillId, scope },
        replace: true,
      });
    }
  }, [agentId, scope, skillId, navigate]);

  return (
    <div className="flex h-full items-center justify-center">
      <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
    </div>
  );
}
