import { useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { User } from "lucide-react";
import { setProfileMemory } from "@/lib/api-client";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/time";
import { MemoryCard } from "./MemoryCard";

interface Props {
  agentId: string;
  content: string;
  updatedAt: string;
}

export function ProfileSection({ agentId, content: initialContent, updatedAt }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [content, setContent] = useState(initialContent);
  const [saving, setSaving] = useState(false);

  const displayContent = content || initialContent;

  const handleSave = useCallback(
    async (newContent: string) => {
      setSaving(true);
      try {
        await setProfileMemory({
          path: { agentId },
          body: { content: newContent },
          throwOnError: true,
        });
        setContent(newContent);
        void queryClient.invalidateQueries({
          queryKey: ["agent-memories", agentId],
        });
      } finally {
        setSaving(false);
      }
    },
    [agentId, queryClient],
  );

  return (
    <MemoryCard
      icon={<User className="size-4" />}
      title={t("memories.profile.title")}
      description={t("memories.profile.description")}
      content={displayContent}
      emptyText={t("memories.profile.empty")}
      placeholder={t("memories.profile.placeholder")}
      saving={saving}
      onSave={handleSave}
      meta={updatedAt ? formatTime(updatedAt) : undefined}
    />
  );
}
