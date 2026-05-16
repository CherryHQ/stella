import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

interface Props {
  agentId: string;
}

interface Memory {
  agent_id: string;
  content: string;
  updated_at: string;
}

export function MemoryPanel({ agentId }: Props) {
  const [content, setContent] = useState("");
  const [updatedAt, setUpdatedAt] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);

  const load = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const memories = await api<Memory[]>("GET", "/api/auth/profile/memories");
      const mem = (memories ?? []).find((m) => m.agent_id === agentId);
      setContent(mem?.content ?? "");
      setUpdatedAt(mem?.updated_at ?? "");
      setDirty(false);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    void load();
  }, [load]);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      await api("PUT", `/api/auth/profile/memories/${encodeURIComponent(agentId)}`, { content });
      setDirty(false);
      setUpdatedAt(new Date().toISOString());
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  }, [agentId, content]);

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
        Loading…
      </div>
    );
  }

  return (
    <div className="flex flex-col flex-1 overflow-hidden">
      <div className="flex-1 overflow-y-auto p-6">
        <div className="mb-4">
          <h2 className="text-base font-semibold">Memory</h2>
          {updatedAt && (
            <p className="text-xs font-mono text-muted-foreground mt-0.5">
              Updated {formatTime(updatedAt)}
            </p>
          )}
        </div>
        <p className="text-xs text-muted-foreground mb-3">
          Persistent context this agent will remember across conversations.
        </p>
        <Textarea
          value={content}
          onChange={(e) => {
            setContent((e.target as HTMLTextAreaElement).value);
            setDirty(true);
          }}
          rows={16}
          placeholder="What should this agent remember? Use natural language or bullet points."
          className="text-sm font-mono"
        />
      </div>
      <div className="flex items-center gap-2 px-6 py-4 border-t border-border flex-shrink-0">
        <Button onClick={() => void save()} disabled={saving || !dirty} size="sm">
          {saving ? "Saving…" : "Save"}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => void load()} disabled={saving}>
          Reset
        </Button>
      </div>
    </div>
  );
}
