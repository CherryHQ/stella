import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Streamdown } from "streamdown";
import { useI18n } from "@/lib/i18n";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { SchedulerJob, SchedulerJobRun } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { SessionConversation } from "@/features/sessions/SessionConversation";

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

function jobScheduleText(job: SchedulerJob): string {
  if (job.cron) return job.cron;
  if (job.every) return `every ${job.every}`;
  if (job.at) return `at ${job.at}`;
  return "unscheduled";
}

function schedulerSessionId(job: SchedulerJob, runs: SchedulerJobRun[]): string {
  const lastRun = runs[0];
  if (lastRun?.session_id) return lastRun.session_id;
  if (job.session_mode !== "new") {
    return `${job.agent_id ? `${job.agent_id}:` : ""}scheduler:${job.id}`;
  }
  return "";
}

function runStatusVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "success") return "success";
  if (status === "error") return "error";
  if (status === "running") return "warning";
  return "outline";
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      {children}
    </label>
  );
}

function ScheduleSummary({ job }: { job: SchedulerJob }) {
  return (
    <div className="rounded-xl border border-border bg-muted/30 p-4">
      <div className="flex flex-wrap gap-2">
        <Badge variant={job.enabled ? "success" : "outline"}>
          {job.enabled ? "enabled" : "disabled"}
        </Badge>
        <Badge variant="outline">{jobScheduleText(job)}</Badge>
        <Badge variant="outline">{job.owner_kind || "user"}</Badge>
        {job.session_mode && <Badge variant="outline">session:{job.session_mode}</Badge>}
      </div>
    </div>
  );
}

function PluginSchedule({ job, label }: { job: SchedulerJob; label: string }) {
  return (
    <details className="rounded-xl border border-border bg-muted/30 p-4">
      <summary className="cursor-pointer text-sm font-semibold">{label}</summary>
      <div className="mt-4 space-y-3">
        <div className="prose prose-sm max-w-none text-foreground [&_ol]:pl-5 [&_ul]:pl-5">
          <Streamdown>{job.description || job.message || ""}</Streamdown>
        </div>
        <div className="flex flex-wrap gap-2">
          {job.plugin_id && <Badge variant="info">plugin:{job.plugin_id}</Badge>}
          {job.job_key && <Badge variant="outline">key:{job.job_key}</Badge>}
          {job.runtime_name && <Badge variant="outline">runtime:{job.runtime_name}</Badge>}
          <Badge variant={job.enabled ? "success" : "outline"}>{job.enabled ? "on" : "off"}</Badge>
        </div>
        {job.payload && Object.keys(job.payload).length > 0 && (
          <pre className="overflow-x-auto rounded-lg bg-muted p-3 text-xs">
            {JSON.stringify(job.payload, null, 2)}
          </pre>
        )}
      </div>
    </details>
  );
}

function ConversationPanel({
  agentId,
  sessionId,
  emptyLabel,
  placeholder,
}: {
  agentId: string;
  sessionId: string;
  emptyLabel: string;
  placeholder: string;
}) {
  if (!sessionId) {
    return (
      <div className="rounded-2xl border border-dashed border-border bg-muted/30 p-4 text-sm text-muted-foreground">
        {emptyLabel}
      </div>
    );
  }
  return (
    <SessionConversation
      agentId={agentId}
      sessionId={sessionId}
      placeholder={placeholder}
      className="h-full min-h-0"
      bodyClassName="min-h-0 flex-1"
    />
  );
}

export function AutomationPanel({ jobId, agentId, onSaved, onDeleted }: Props) {
  const { t } = useI18n();
  const isNew = jobId === null;
  const [job, setJob] = useState<SchedulerJob | null>(null);
  const [savedForm, setSavedForm] = useState<Form>(emptyForm());
  const [form, setForm] = useState<Form>(emptyForm());
  const [runs, setRuns] = useState<SchedulerJobRun[]>([]);
  const [loading, setLoading] = useState(!isNew);
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const patchForm = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));

  const load = useCallback(async () => {
    if (!jobId) return;
    setLoading(true);
    try {
      const [j, jobRuns] = await Promise.all([
        api<SchedulerJob>(
          "GET",
          `/api/agents/${encodeURIComponent(agentId)}/scheduler/jobs/${encodeURIComponent(jobId)}`,
        ),
        api<SchedulerJobRun[]>(
          "GET",
          `/api/agents/${encodeURIComponent(agentId)}/scheduler/jobs/${encodeURIComponent(jobId)}/runs`,
        ).catch(() => []),
      ]);
      const f: Form = {
        name: j.name,
        schedule_type: j.cron ? "cron" : "every",
        cron: j.cron ?? "",
        every: j.every ?? "",
        message: j.message ?? "",
        session_mode: j.session_mode || "reuse",
        enabled: j.enabled,
      };
      setJob(j);
      setForm(f);
      setSavedForm(f);
      setRuns(jobRuns ?? []);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [jobId]);

  useEffect(() => {
    if (isNew) {
      const f = emptyForm();
      setJob(null);
      setForm(f);
      setSavedForm(f);
      setRuns([]);
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
        await api("POST", `/api/agents/${encodeURIComponent(agentId)}/scheduler/jobs`, payload);
      } else {
        await api(
          "PUT",
          `/api/agents/${encodeURIComponent(agentId)}/scheduler/jobs/${encodeURIComponent(jobId!)}`,
          payload,
        );
      }
      setSavedForm(form);
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
      await api(
        "DELETE",
        `/api/agents/${encodeURIComponent(agentId)}/scheduler/jobs/${encodeURIComponent(jobId)}`,
      );
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
      await api(
        "POST",
        `/api/agents/${encodeURIComponent(agentId)}/scheduler/jobs/${encodeURIComponent(jobId)}/run`,
      );
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
        {t("common.loading")}
      </div>
    );
  }

  const isReadOnly = !isNew && job !== null && job.owner_kind !== "user";
  const dirty = JSON.stringify(form) !== JSON.stringify(savedForm);
  const isValid = Boolean(
    form.name && form.message && (form.schedule_type === "cron" ? form.cron : form.every),
  );
  const sessionId = job ? schedulerSessionId(job, runs) : "";

  return (
    <div className="flex flex-col flex-1 overflow-hidden">
      <div className="flex-1 overflow-y-auto p-6 space-y-5">
        {/* Header */}
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground">
              {isNew
                ? t("sessions.auto.eyebrowNew")
                : isReadOnly
                  ? t("sessions.auto.eyebrowSystem")
                  : t("sessions.auto.eyebrowUser")}
            </div>
            <h2 className="mt-1.5 font-serif text-2xl italic tracking-tight truncate">
              {isNew ? t("sessions.auto.titleNew") : form.name}
            </h2>
            {!isNew && job && (
              <div className="mt-1 font-mono text-xs text-muted-foreground truncate">{job.id}</div>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {!isNew && (
              <Button variant="outline" size="sm" onClick={() => void runNow()} disabled={running}>
                {running ? t("sessions.auto.running") : t("sessions.auto.runNow")}
              </Button>
            )}
            {!isNew && !isReadOnly && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void remove()}
                disabled={deleting}
                className="text-destructive hover:text-destructive"
              >
                {deleting ? t("sessions.auto.deleting") : t("common.delete")}
              </Button>
            )}
          </div>
        </div>

        {/* Summary badges */}
        {!isNew && job && <ScheduleSummary job={job} />}

        {/* System/plugin job: Streamdown definition */}
        {!isNew && job && isReadOnly && (
          <PluginSchedule job={job} label={t("sessions.auto.scheduleDefinition")} />
        )}

        {/* User job or new: editable form in collapsible */}
        {!isReadOnly && (
          <details className="rounded-xl border border-border bg-muted/30 p-4" open={isNew}>
            <summary className="cursor-pointer text-sm font-semibold">
              {isNew ? t("sessions.auto.settingsNew") : t("sessions.auto.settingsEdit")}
            </summary>
            <div className="mt-4 space-y-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={t("sessions.auto.fieldName")}>
                  <Input
                    nativeInput
                    value={form.name}
                    onChange={(e) => patchForm({ name: (e.target as HTMLInputElement).value })}
                    placeholder={t("sessions.auto.namePlaceholder")}
                    className="text-sm"
                  />
                </Field>
                <Field label={t("sessions.auto.fieldSessionMode")}>
                  <select
                    value={form.session_mode}
                    onChange={(e) => patchForm({ session_mode: e.target.value })}
                    className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
                  >
                    <option value="reuse">{t("sessions.auto.reuseSession")}</option>
                    <option value="new">{t("sessions.auto.newSessionMode")}</option>
                  </select>
                </Field>
              </div>

              <Field label={t("sessions.auto.fieldSchedule")}>
                <div className="mb-2 flex items-center gap-4">
                  {(["cron", "every"] as const).map((schedType) => (
                    <label
                      key={schedType}
                      className="flex cursor-pointer items-center gap-2 text-sm"
                    >
                      <input
                        type="radio"
                        name="schedule_type"
                        checked={form.schedule_type === schedType}
                        onChange={() => patchForm({ schedule_type: schedType })}
                        className="accent-primary"
                      />
                      {schedType === "cron"
                        ? t("sessions.auto.tabCron")
                        : t("sessions.auto.tabInterval")}
                    </label>
                  ))}
                </div>
                {form.schedule_type === "cron" ? (
                  <Input
                    nativeInput
                    value={form.cron}
                    onChange={(e) => patchForm({ cron: (e.target as HTMLInputElement).value })}
                    placeholder={t("sessions.auto.cronPlaceholder")}
                    className="text-sm font-mono"
                  />
                ) : (
                  <Input
                    nativeInput
                    value={form.every}
                    onChange={(e) => patchForm({ every: (e.target as HTMLInputElement).value })}
                    placeholder={t("sessions.auto.intervalPlaceholder")}
                    className="text-sm font-mono"
                  />
                )}
              </Field>

              <Field label={t("sessions.auto.fieldMessage")}>
                <Textarea
                  value={form.message}
                  onChange={(e) => patchForm({ message: (e.target as HTMLTextAreaElement).value })}
                  rows={4}
                  placeholder={t("sessions.auto.messagePlaceholder")}
                  className="text-sm"
                />
              </Field>

              <div
                className={cn(
                  "flex items-center border-t border-border pt-4",
                  isNew ? "justify-end" : "justify-between",
                )}
              >
                {!isNew && (
                  <label className="flex items-center gap-2 text-sm cursor-pointer">
                    <Switch
                      checked={form.enabled}
                      onCheckedChange={(checked) => patchForm({ enabled: checked })}
                    />
                    {t("sessions.auto.fieldEnabled")}
                  </label>
                )}
                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    disabled={saving || !isValid || (!dirty && !isNew)}
                    onClick={() => void save()}
                  >
                    {saving
                      ? t("sessions.auto.saving")
                      : isNew
                        ? t("common.create")
                        : t("common.save")}
                  </Button>
                  {!isNew && (
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={saving}
                      onClick={() => setForm(savedForm)}
                    >
                      {t("sessions.auto.reset")}
                    </Button>
                  )}
                </div>
              </div>
            </div>
          </details>
        )}

        {/* Run history */}
        {!isNew && runs.length > 0 && (
          <div className="pt-1">
            <p className="text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground mb-3">
              {t("sessions.auto.recentRuns")}
            </p>
            <div className="divide-y divide-border rounded-xl border border-border overflow-hidden">
              {runs.map((r) => (
                <div key={r.id} className="flex items-center gap-3 px-4 py-2.5 text-xs font-mono">
                  <Badge variant={runStatusVariant(r.status)} size="sm">
                    {r.status}
                  </Badge>
                  <span className="text-muted-foreground">{formatTime(r.started_at)}</span>
                  {r.duration && <span className="text-muted-foreground/60">{r.duration}</span>}
                  {r.error && (
                    <span className="text-destructive truncate max-w-[180px]" title={r.error}>
                      {r.error}
                    </span>
                  )}
                  {r.session_id && (
                    <a
                      href={`/sessions/${encodeURIComponent(r.session_id)}`}
                      className="text-primary hover:underline ml-auto"
                    >
                      → session
                    </a>
                  )}
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Conversation panel */}
        {!isNew && (
          <div className="pt-1">
            <p className="text-[11px] font-mono font-medium uppercase tracking-wider text-muted-foreground mb-3">
              {t("sessions.auto.conversation")}
            </p>
            <div
              className="rounded-xl border border-border overflow-hidden"
              style={{ minHeight: 240 }}
            >
              <ConversationPanel
                agentId={agentId}
                sessionId={sessionId}
                emptyLabel={t("sessions.auto.noSession")}
                placeholder={t("sessions.auto.askPlaceholder")}
              />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
