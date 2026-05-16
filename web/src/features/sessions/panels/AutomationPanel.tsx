import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { SchedulerJob, SchedulerJobRun } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

interface Props {
  jobId: string | null;
  agentId: string;
  onSaved: () => void;
  onDeleted: () => void;
}

interface Form {
  name: string;
  schedule_type: "cron" | "every";
  cron: string;
  every: string;
  message: string;
  session_mode: string;
  enabled: boolean;
}

function emptyForm(): Form {
  return {
    name: "",
    schedule_type: "cron",
    cron: "",
    every: "",
    message: "",
    session_mode: "reuse",
    enabled: true,
  };
}

function runStatusVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "success") return "success";
  if (status === "error") return "error";
  if (status === "running") return "warning";
  return "outline";
}

export function AutomationPanel({ jobId, agentId, onSaved, onDeleted }: Props) {
  const isNew = jobId === null;
  const [form, setForm] = useState<Form>(emptyForm());
  const [runs, setRuns] = useState<SchedulerJobRun[]>([]);
  const [loading, setLoading] = useState(!isNew);
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [dirty, setDirty] = useState(false);

  const patchForm = (patch: Partial<Form>) => {
    setForm((f) => ({ ...f, ...patch }));
    setDirty(true);
  };

  const load = useCallback(async () => {
    if (!jobId) return;
    setLoading(true);
    try {
      const [job, jobRuns] = await Promise.all([
        api<SchedulerJob>("GET", `/api/scheduler/jobs/${encodeURIComponent(jobId)}`),
        api<SchedulerJobRun[]>(
          "GET",
          `/api/scheduler/jobs/${encodeURIComponent(jobId)}/runs`,
        ).catch(() => []),
      ]);
      setForm({
        name: job.name,
        schedule_type: job.cron ? "cron" : "every",
        cron: job.cron ?? "",
        every: job.every ?? "",
        message: job.message ?? "",
        session_mode: job.session_mode || "reuse",
        enabled: job.enabled,
      });
      setRuns(jobRuns ?? []);
      setDirty(false);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [jobId]);

  useEffect(() => {
    if (isNew) {
      setForm(emptyForm());
      setRuns([]);
      setDirty(false);
    } else {
      void load();
    }
  }, [isNew, agentId, load]);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      const payload = {
        name: form.name,
        message: form.message,
        cron: form.schedule_type === "cron" ? form.cron : "",
        every: form.schedule_type === "every" ? form.every : "",
        session_mode: form.session_mode,
        enabled: form.enabled,
        agent_id: agentId,
      };
      if (isNew) {
        await api("POST", "/api/scheduler/jobs", payload);
      } else {
        await api("PUT", `/api/scheduler/jobs/${encodeURIComponent(jobId!)}`, payload);
      }
      setDirty(false);
      onSaved();
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  }, [isNew, jobId, agentId, form, onSaved]);

  const remove = useCallback(async () => {
    if (!jobId) return;
    setDeleting(true);
    try {
      await api("DELETE", `/api/scheduler/jobs/${encodeURIComponent(jobId)}`);
      onDeleted();
    } catch (e) {
      console.error(e);
    } finally {
      setDeleting(false);
    }
  }, [jobId, onDeleted]);

  const runNow = useCallback(async () => {
    if (!jobId) return;
    setRunning(true);
    try {
      await api("POST", `/api/scheduler/jobs/${encodeURIComponent(jobId)}/run`);
      await load();
    } catch (e) {
      console.error(e);
    } finally {
      setRunning(false);
    }
  }, [jobId, load]);

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
        Loading…
      </div>
    );
  }

  return (
    <div className="flex flex-col flex-1 overflow-hidden">
      <div className="flex-1 overflow-y-auto p-6 space-y-5">
        {/* Header */}
        <div className="flex items-start justify-between">
          <h2 className="text-base font-semibold">{isNew ? "New Automation" : form.name}</h2>
          <div className="flex items-center gap-2">
            {!isNew && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => void runNow()}
                  disabled={running}
                >
                  {running ? "Running…" : "Run Now"}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => void remove()}
                  disabled={deleting}
                  className="text-destructive hover:text-destructive"
                >
                  {deleting ? "Deleting…" : "Delete"}
                </Button>
              </>
            )}
          </div>
        </div>

        {/* Form */}
        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="block text-sm font-mono mb-1">Name</label>
            <Input
              nativeInput
              value={form.name}
              onChange={(e) => patchForm({ name: (e.target as HTMLInputElement).value })}
              placeholder="Daily standup"
              className="text-sm"
            />
          </div>
          <div>
            <label className="block text-sm font-mono mb-1">Session Mode</label>
            <select
              value={form.session_mode}
              onChange={(e) => patchForm({ session_mode: e.target.value })}
              className="w-full rounded-lg border border-input bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
            >
              <option value="reuse">Reuse session</option>
              <option value="new">New session each run</option>
            </select>
          </div>

          <div className="col-span-2">
            <label className="block text-sm font-mono mb-1">Schedule</label>
            <div className="flex bg-muted rounded-lg border border-border p-0.5 mb-2 gap-0.5">
              {(["cron", "every"] as const).map((t) => (
                <button
                  key={t}
                  type="button"
                  onClick={() => patchForm({ schedule_type: t })}
                  className={cn(
                    "flex-1 text-sm rounded-md py-1 transition-colors",
                    form.schedule_type === t
                      ? "bg-background text-foreground shadow-sm"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {t === "cron" ? "Cron" : "Interval"}
                </button>
              ))}
            </div>
            {form.schedule_type === "cron" ? (
              <Input
                nativeInput
                value={form.cron}
                onChange={(e) => patchForm({ cron: (e.target as HTMLInputElement).value })}
                placeholder="0 9 * * 1-5"
                className="text-sm font-mono"
              />
            ) : (
              <Input
                nativeInput
                value={form.every}
                onChange={(e) => patchForm({ every: (e.target as HTMLInputElement).value })}
                placeholder="1h30m"
                className="text-sm font-mono"
              />
            )}
          </div>

          <div className="col-span-2">
            <label className="block text-sm font-mono mb-1">Message</label>
            <Textarea
              value={form.message}
              onChange={(e) => patchForm({ message: (e.target as HTMLTextAreaElement).value })}
              rows={4}
              placeholder="What should the agent do?"
              className="text-sm"
            />
          </div>
        </div>

        <div>
          <label className="flex items-center gap-3 cursor-pointer">
            <Switch
              checked={form.enabled}
              onCheckedChange={(checked) => patchForm({ enabled: checked })}
            />
            <span className="text-sm">Enabled</span>
          </label>
        </div>

        {/* Run history */}
        {!isNew && runs.length > 0 && (
          <div className="pt-2 border-t border-border">
            <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-3">
              Recent Runs
            </p>
            <div className="space-y-0 divide-y divide-border">
              {runs.map((r) => (
                <div key={r.id} className="flex items-center gap-3 py-2.5 text-xs font-mono">
                  <Badge variant={runStatusVariant(r.status)} size="sm">
                    {r.status}
                  </Badge>
                  <span className="text-muted-foreground">{formatTime(r.started_at)}</span>
                  {r.duration && <span className="text-muted-foreground/60">{r.duration}</span>}
                  {r.session_id && (
                    <a
                      href={`/sessions/${encodeURIComponent(r.session_id)}`}
                      className="text-primary hover:underline ml-auto"
                    >
                      → session
                    </a>
                  )}
                  {r.error && (
                    <span className="text-destructive truncate max-w-[200px]" title={r.error}>
                      {r.error}
                    </span>
                  )}
                </div>
              ))}
            </div>
          </div>
        )}
      </div>

      <div className="flex items-center gap-2 px-6 py-4 border-t border-border flex-shrink-0">
        <Button onClick={() => void save()} disabled={saving || (!dirty && !isNew)} size="sm">
          {saving ? "Saving…" : isNew ? "Create" : "Save"}
        </Button>
        {!isNew && (
          <Button variant="ghost" size="sm" onClick={() => void load()} disabled={saving}>
            Reset
          </Button>
        )}
      </div>
    </div>
  );
}
