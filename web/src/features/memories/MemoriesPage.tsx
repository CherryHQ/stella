import { useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useAppShell } from "@/layouts/AppShell";
import { useEffect } from "react";
import { agentMemoriesOptions } from "@/lib/queries/agents";
import { SoulSection } from "./SoulSection";
import { ProfileSection } from "./ProfileSection";
import { ConstraintsSection } from "./ConstraintsSection";
import { ChangelogSection } from "./ChangelogSection";

export function MemoriesPage() {
  const { agentId } = useParams({ strict: false }) as { agentId: string };
  const { setHeaderTitle, setHeaderActions } = useAppShell();
  const { data: memories = [] } = useQuery(agentMemoriesOptions(agentId));

  const memory = memories[0] as
    | {
        agent_id: string;
        content: string;
        soul: string;
        version: number;
        constraints: string;
        updated_at: string;
      }
    | undefined;

  useEffect(() => {
    setHeaderTitle(<span className="text-sm font-medium">Memories</span>);
    setHeaderActions(null);
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [setHeaderActions, setHeaderTitle]);

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10 space-y-6">
        <SoulSection agentId={agentId} soul={memory?.soul ?? ""} />

        <ProfileSection
          agentId={agentId}
          content={memory?.content ?? ""}
          updatedAt={memory?.updated_at ?? ""}
        />

        <ConstraintsSection agentId={agentId} />

        <ChangelogSection agentId={agentId} />
      </div>
    </div>
  );
}
