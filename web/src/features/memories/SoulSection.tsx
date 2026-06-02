import { useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Sparkles } from "lucide-react";
import { setProfileSoul } from "@/lib/api-client";
import { useI18n } from "@/lib/i18n";
import { MemoryCard } from "./MemoryCard";

interface Props {
  agentId: string;
  soul: string;
}

export function SoulSection({ agentId, soul: initialSoul }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [soul, setSoul] = useState(initialSoul);
  const [saving, setSaving] = useState(false);

  // Sync from parent when data reloads
  const displaySoul = soul || initialSoul;

  const handleSave = useCallback(
    async (content: string) => {
      setSaving(true);
      try {
        await setProfileSoul({
          path: { agentId },
          body: { soul: content },
          throwOnError: true,
        });
        setSoul(content);
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
      icon={<Sparkles className="size-4" />}
      title={t("memories.soul.title")}
      description={t("memories.soul.description")}
      content={displaySoul}
      emptyText={t("memories.soul.empty")}
      placeholder={t("memories.soul.placeholder")}
      saving={saving}
      onSave={handleSave}
    />
  );
}
