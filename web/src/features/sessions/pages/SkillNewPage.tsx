import { useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";

export function SkillNewPage() {
  const { agentId } = useParams({ strict: false }) as { agentId: string };
  const navigate = useNavigate();

  useEffect(() => {
    if (agentId) {
      void navigate({
        to: "/agents/$agentId/skills",
        params: { agentId },
        search: { new: true },
        replace: true,
      });
    }
  }, [agentId, navigate]);

  return (
    <div className="flex h-full items-center justify-center">
      <div className="size-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
    </div>
  );
}
