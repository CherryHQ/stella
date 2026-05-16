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
  const [draft, setDraft] = useState("");
  const [updatedAt, setUpdatedAt] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [editing, setEditing] = useState(false);

  const load = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const memories = await api<Memory[]>("GET", "/api/auth/profile/memories");
      const mem = (memories ?? []).find((m) => m.agent_id === agentId);
      setContent(mem?.content ?? "");
      setUpdatedAt(mem?.updated_at ?? "");
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
    setDraft(content);
    setEditing(true);
  };

  const cancelEdit = () => {
    setEditing(false);
  };

  const save = useCallback(async () => {
    setSaving(true);
    try {
      await api("PUT", `/api/auth/profile/memories/${encodeURIComponent(agentId)}`, {
        content: draft,
      });
      setContent(draft);
      setUpdatedAt(new Date().toISOString());
      setEditing(false);
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  }, [agentId, draft]);

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
            <h2 className="text-base font-semibold">User Profile</h2>
            {updatedAt && (
              <p className="text-xs font-mono text-muted-foreground mt-0.5">
                Updated {formatTime(updatedAt)}
              </p>
            )}
          </div>
          {!editing && (
            <Button variant="outline" size="sm" onClick={startEdit}>
              Edit
            </Button>
          )}
        </div>
        <p className="text-xs text-muted-foreground mb-3">
          Persistent context this agent will remember across conversations.
        </p>

        {editing ? (
          <Textarea
            value={draft}
            onChange={(e) => {
              setDraft((e.target as HTMLTextAreaElement).value);
            }}
            rows={16}
            placeholder="What should this agent remember? Use natural language or bullet points."
            className="text-sm font-mono"
            autoFocus
          />
        ) : content ? (
          <pre className="text-sm font-mono whitespace-pre-wrap text-foreground/90 leading-relaxed">
            {content}
          </pre>
        ) : (
          <p className="text-sm text-muted-foreground italic">No memory yet.</p>
        )}
      </div>

      {editing && (
        <div className="flex items-center gap-2 px-6 py-4 border-t border-border flex-shrink-0">
          <Button onClick={() => void save()} disabled={saving || draft === content} size="sm">
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
