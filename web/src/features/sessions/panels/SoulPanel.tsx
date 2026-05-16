import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { AgentDetail } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

interface Props {
  agentId: string;
}

export function SoulPanel({ agentId }: Props) {
  const [agent, setAgent] = useState<AgentDetail | null>(null);
  const [soul, setSoul] = useState("");
  const [draft, setDraft] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [editing, setEditing] = useState(false);

  const load = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const a = await api<AgentDetail>("GET", `/api/agents/${encodeURIComponent(agentId)}`);
      setAgent(a);
      setSoul(a.soul ?? "");
      setEditing(false);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    void load();
  }, [load]);

  const startEdit = () => {
    setDraft(soul);
    setEditing(true);
  };

  const cancelEdit = () => {
    setEditing(false);
  };

  const save = useCallback(async () => {
    if (!agent) return;
    setSaving(true);
    try {
      await api("PUT", `/api/agents/${encodeURIComponent(agentId)}`, { ...agent, soul: draft });
      setSoul(draft);
      setAgent((prev) => (prev ? { ...prev, soul: draft } : prev));
      setEditing(false);
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  }, [agentId, agent, draft]);

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
        <div className="flex items-start justify-between mb-4">
          <div>
            <h2 className="text-base font-semibold">Agent Soul</h2>
            <p className="text-xs text-muted-foreground mt-0.5">
              Default personality and behavior tone for this agent.
            </p>
          </div>
          {!editing && (
            <Button variant="outline" size="sm" onClick={startEdit}>
              Edit
            </Button>
          )}
        </div>

        {editing ? (
          <Textarea
            value={draft}
            onChange={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
            rows={16}
            placeholder="Describe the agent's personality, tone, and behavior…"
            className="text-sm font-mono"
            autoFocus
          />
        ) : soul ? (
          <pre className="text-sm font-mono whitespace-pre-wrap text-foreground/90 leading-relaxed">
            {soul}
          </pre>
        ) : (
          <p className="text-sm text-muted-foreground italic">No soul configured.</p>
        )}
      </div>

      {editing && (
        <div className="flex items-center gap-2 px-6 py-4 border-t border-border flex-shrink-0">
          <Button onClick={() => void save()} disabled={saving || draft === soul} size="sm">
            {saving ? "Saving…" : "Save"}
          </Button>
          <Button variant="ghost" size="sm" onClick={cancelEdit} disabled={saving}>
            Cancel
          </Button>
        </div>
      )}
    </div>
  );
}
