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
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);

  const load = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const a = await api<AgentDetail>("GET", `/api/agents/${encodeURIComponent(agentId)}`);
      setAgent(a);
      setSoul(a.soul ?? "");
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
    if (!agent) return;
    setSaving(true);
    try {
      await api("PUT", `/api/agents/${encodeURIComponent(agentId)}`, { ...agent, soul });
      setAgent((prev) => (prev ? { ...prev, soul } : prev));
      setDirty(false);
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  }, [agentId, agent, soul]);

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
          <h2 className="text-base font-semibold">Agent Soul</h2>
        </div>
        <p className="text-xs text-muted-foreground mb-3">
          Default personality and behavior tone for this agent.
        </p>
        <Textarea
          value={soul}
          onChange={(e) => {
            setSoul((e.target as HTMLTextAreaElement).value);
            setDirty(true);
          }}
          rows={16}
          placeholder="Describe the agent's personality, tone, and behavior…"
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
