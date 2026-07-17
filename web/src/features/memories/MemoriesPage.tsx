import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useAppShell } from "@/layouts/AppShell";
import { agentMemoryOptions } from "@/lib/queries/memories";
import type { MemorySearch } from "@/lib/route-search";
import { SoulSection } from "./SoulSection";
import { ProfileSection } from "./ProfileSection";
import { KnowledgeSection } from "./KnowledgeSection";
import { ConstraintsSection } from "./ConstraintsSection";
import { ChangelogSection } from "./ChangelogSection";

export function MemoriesPage() {
  const { agentId, projectId } = useParams({ strict: false }) as {
    agentId: string;
    projectId?: string;
  };
  const search = useSearch({ strict: false }) as MemorySearch;
  const navigate = useNavigate();
  const { setHeaderActions } = useAppShell();
  const { data: memory } = useQuery(agentMemoryOptions(agentId));
  const knowledgeState = search.knowledge === "removed" ? "removed" : "active";

  useEffect(() => {
    setHeaderActions(null);
    return () => {
      setHeaderActions(null);
    };
  }, [setHeaderActions]);

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto max-w-3xl p-6 sm:p-8 lg:p-10">
        <SoulSection agentId={agentId} soul={memory?.soul ?? ""} />
        <ProfileSection
          agentId={agentId}
          content={memory?.content ?? ""}
          updatedAt={memory?.updated_at ?? ""}
        />
        <KnowledgeSection
          agentId={agentId}
          state={knowledgeState}
          onStateChange={(state) => {
            // Keep the selected lifecycle view shareable and browser-history aware.
            const search = { knowledge: state === "removed" ? ("removed" as const) : undefined };
            if (projectId) {
              void navigate({
                to: "/agents/$agentId/projects/$projectId/memories",
                params: { agentId, projectId },
                search,
              });
            } else {
              void navigate({
                to: "/agents/$agentId/memories",
                params: { agentId },
                search,
              });
            }
          }}
        />
        <ConstraintsSection agentId={agentId} />
        <ChangelogSection agentId={agentId} />
      </div>
    </div>
  );
}
