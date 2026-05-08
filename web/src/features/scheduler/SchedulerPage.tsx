import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { Agent, SchedulerJob, SchedulerJobList, SchedulerJobRun } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";

interface JobForm {
  name: string;
  cron: string;
  every: string;
  message: string;
  session_mode: string;
  enabled: boolean;
  agent_id: string;
  schedule_type: "cron" | "every";
  system_job: boolean;
}

const emptyForm = (): JobForm => ({
  name: "",
  cron: "",
  every: "",
  message: "",
  session_mode: "reuse",
  enabled: true,
  agent_id: "",
  schedule_type: "cron",
  system_job: false,
});

function jobScheduleText(j: SchedulerJob): string {
  if (j.cron) return j.cron;
  if (j.every) return "every " + j.every;
  if (j.at) return "at " + j.at;
  return "unscheduled";
}

function statusBadgeVariant(status: string): "success" | "error" | "warning" | "outline" {
  if (status === "success") return "success";
  if (status === "error") return "error";
  if (status === "running") return "warning";
  return "outline";
}

interface Toast {
  msg: string;
  kind: "success" | "error";
}
interface ConfirmState {
  msg: string;
  action: () => void;
}

export function SchedulerPage() {
  const [jobs, setJobs] = useState<SchedulerJob[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [isAdmin, setIsAdmin] = useState(false);
  const [editingJobId, setEditingJobId] = useState<number | null>(null);
  const [expandedJobId, setExpandedJobId] = useState<number | null>(null);
  const [triggeringJobId, setTriggeringJobId] = useState<number | null>(null);
  const [runHistories, setRunHistories] = useState<Record<number, SchedulerJobRun[]>>({});
  const [jobForm, setJobForm] = useState<JobForm>(emptyForm());
  const [toast, setToast] = useState<Toast | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState | null>(null);

  const showToast = useCallback((msg: string, kind: "success" | "error" = "success") => {
    setToast({ msg, kind });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const loadJobs = useCallback(async () => {
    try {
      const list = await api<SchedulerJobList>("GET", "/api/scheduler/jobs");
      setJobs(list.items || []);
    } catch (e) {
      console.error(e);
    }
  }, []);

  const loadAgents = useCallback(async () => {
    try {
      const list = await api<Agent[]>("GET", "/api/agents");
      setAgents(list || []);
    } catch (e) {
      console.error(e);
    }
  }, []);

  const loadMe = useCallback(async () => {
    try {
      const me = await api<{ is_admin: boolean }>("GET", "/api/auth/me");
      setIsAdmin(me.is_admin || false);
    } catch {
      setIsAdmin(false);
    }
  }, []);

  useEffect(() => {
    void Promise.all([loadJobs(), loadAgents(), loadMe()]);
  }, [loadJobs, loadAgents, loadMe]);

  const resetForm = useCallback(() => {
    setJobForm(emptyForm());
    setEditingJobId(null);
  }, []);

  const editJob = useCallback((j: SchedulerJob) => {
    if (j.owner_kind === "plugin") return;
    setEditingJobId(j.id);
    setJobForm({
      name: j.name,
      message: j.message,
      schedule_type: j.cron ? "cron" : "every",
      cron: j.cron || "",
      every: j.every || "",
      session_mode: j.session_mode || "reuse",
      enabled: j.enabled,
      agent_id: j.agent_id || "",
      system_job: !j.user_id,
    });
  }, []);

  const saveJob = useCallback(async () => {
    const payload: Record<string, unknown> = {
      name: jobForm.name,
      message: jobForm.message,
      cron: jobForm.schedule_type === "cron" ? jobForm.cron : "",
      every: jobForm.schedule_type === "every" ? jobForm.every : "",
      session_mode: jobForm.session_mode,
      enabled: jobForm.enabled,
      agent_id: jobForm.agent_id,
    };
    if (isAdmin && jobForm.system_job) payload.user_id = 0;
    try {
      if (editingJobId !== null) {
        await api("PUT", "/api/scheduler/jobs/" + editingJobId, payload);
      } else {
        await api("POST", "/api/scheduler/jobs", payload);
      }
      resetForm();
      await loadJobs();
      showToast("Saved");
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    }
  }, [jobForm, isAdmin, editingJobId, resetForm, loadJobs, showToast]);

  const toggleJob = useCallback(
    async (j: SchedulerJob) => {
      if (j.owner_kind === "plugin") return;
      try {
        await api("PUT", "/api/scheduler/jobs/" + j.id, {
          name: j.name,
          message: j.message,
          cron: j.cron || "",
          every: j.every || "",
          session_mode: j.session_mode,
          enabled: !j.enabled,
          agent_id: j.agent_id || "",
        });
        await loadJobs();
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      }
    },
    [loadJobs, showToast],
  );

  const doDeleteJob = useCallback(
    async (id: number) => {
      const job = jobs.find((item) => item.id === id);
      if (job?.owner_kind === "plugin") return;
      try {
        await api("DELETE", "/api/scheduler/jobs/" + id);
        await loadJobs();
        showToast("Deleted");
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      }
    },
    [jobs, loadJobs, showToast],
  );

  const triggerJob = useCallback(
    async (j: SchedulerJob) => {
      setTriggeringJobId(j.id);
      try {
        await api("POST", "/api/scheduler/jobs/" + j.id + "/run");
        showToast("Job triggered");
        if (expandedJobId === j.id) {
          const runs = await api<SchedulerJobRun[]>("GET", "/api/scheduler/jobs/" + j.id + "/runs");
          setRunHistories((prev) => ({ ...prev, [j.id]: runs || [] }));
        }
        await loadJobs();
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      } finally {
        setTriggeringJobId(null);
      }
    },
    [expandedJobId, loadJobs, showToast],
  );

  const toggleRuns = useCallback(
    async (jobId: number) => {
      if (expandedJobId === jobId) {
        setExpandedJobId(null);
        return;
      }
      setExpandedJobId(jobId);
      try {
        const runs = await api<SchedulerJobRun[]>("GET", "/api/scheduler/jobs/" + jobId + "/runs");
        setRunHistories((prev) => ({ ...prev, [jobId]: runs || [] }));
      } catch (e) {
        console.error(e);
      }
    },
    [expandedJobId],
  );

  const isFormValid =
    jobForm.name &&
    jobForm.message &&
    (jobForm.schedule_type === "cron" ? !!jobForm.cron : !!jobForm.every);

  return (
    <div>
      <div className="mb-8">
        <h1 className="font-serif text-2xl font-normal tracking-tight mb-1">Scheduled tasks</h1>
        <p className="text-sm text-muted-foreground">
          Recurring jobs that Anna executes on a schedule.
        </p>
      </div>

      <div className="border-t border-border pt-8">
        {/* Job form */}
        <div className="mb-8 pb-8 border-b border-border">
          <div className="flex items-center justify-between mb-4">
            <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider">
              {editingJobId ? "EDIT JOB" : "NEW JOB"}
            </p>
            {editingJobId && (
              <Button variant="ghost" size="xs" onClick={resetForm}>
                Cancel
              </Button>
            )}
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-8 gap-y-4 mb-4">
            <div className="space-y-1.5">
              <label className="block text-xs font-mono text-muted-foreground uppercase tracking-wider">
                Name
              </label>
              <Input
                type="text"
                value={jobForm.name}
                onChange={(e) => setJobForm((f) => ({ ...f, name: e.target.value }))}
                placeholder="Daily summary"
                nativeInput
              />
            </div>
            <div className="space-y-1.5">
              <label className="block text-xs font-mono text-muted-foreground uppercase tracking-wider">
                Session Mode
              </label>
              <select
                value={jobForm.session_mode}
                onChange={(e) => setJobForm((f) => ({ ...f, session_mode: e.target.value }))}
                className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
              >
                <option value="reuse">Reuse session</option>
                <option value="new">New session each run</option>
              </select>
            </div>
          </div>

          <div className="mb-4 space-y-1.5">
            <label className="block text-xs font-mono text-muted-foreground uppercase tracking-wider">
              Schedule
            </label>
            <div className="flex items-center gap-4 mb-2">
              <label className="flex items-center gap-2 text-sm cursor-pointer">
                <input
                  type="radio"
                  name="schedule_type"
                  value="cron"
                  checked={jobForm.schedule_type === "cron"}
                  onChange={() => setJobForm((f) => ({ ...f, schedule_type: "cron" }))}
                  className="accent-primary"
                />
                <span>Cron</span>
              </label>
              <label className="flex items-center gap-2 text-sm cursor-pointer">
                <input
                  type="radio"
                  name="schedule_type"
                  value="every"
                  checked={jobForm.schedule_type === "every"}
                  onChange={() => setJobForm((f) => ({ ...f, schedule_type: "every" }))}
                  className="accent-primary"
                />
                <span>Interval</span>
              </label>
            </div>
            {jobForm.schedule_type === "cron" ? (
              <Input
                type="text"
                value={jobForm.cron}
                onChange={(e) => setJobForm((f) => ({ ...f, cron: e.target.value }))}
                placeholder="0 9 * * 1-5"
                className="font-mono"
                nativeInput
              />
            ) : (
              <Input
                type="text"
                value={jobForm.every}
                onChange={(e) => setJobForm((f) => ({ ...f, every: e.target.value }))}
                placeholder="30m, 2h"
                className="font-mono"
                nativeInput
              />
            )}
          </div>

          <div className="mb-4 space-y-1.5">
            <label className="block text-xs font-mono text-muted-foreground uppercase tracking-wider">
              Agent
            </label>
            <select
              value={jobForm.agent_id}
              onChange={(e) => setJobForm((f) => ({ ...f, agent_id: e.target.value }))}
              className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
            >
              <option value="">Default agent</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </div>

          {isAdmin && (
            <div className="mb-4 flex items-center gap-2">
              <Switch
                checked={jobForm.system_job}
                onCheckedChange={(v) => setJobForm((f) => ({ ...f, system_job: v }))}
              />
              <span className="text-sm">System job</span>
              <span className="text-xs text-muted-foreground">(broadcasts to all users)</span>
            </div>
          )}

          <div className="mb-4 space-y-1.5">
            <label className="block text-xs font-mono text-muted-foreground uppercase tracking-wider">
              Message
            </label>
            <Textarea
              value={jobForm.message}
              onChange={(e) => setJobForm((f) => ({ ...f, message: e.target.value }))}
              placeholder="What should the agent do?"
            />
          </div>

          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Switch
                checked={jobForm.enabled}
                onCheckedChange={(v) => setJobForm((f) => ({ ...f, enabled: v }))}
              />
              <span className="text-sm">Enabled</span>
            </div>
            <Button size="sm" disabled={!isFormValid} onClick={saveJob}>
              {editingJobId ? "Update" : "Create"}
            </Button>
          </div>
        </div>

        {/* Job list */}
        <div className="divide-y divide-border">
          {jobs.map((j) => (
            <div key={j.id} className="py-5 group">
              <div className="flex items-baseline justify-between gap-4">
                <div className="flex items-baseline gap-3 flex-wrap">
                  <span className="font-medium">{j.name}</span>
                  <Badge size="sm" variant={j.enabled ? "success" : "outline"}>
                    {j.enabled ? "on" : "off"}
                  </Badge>
                  <span className="text-xs font-mono text-muted-foreground">
                    {jobScheduleText(j)}
                  </span>
                  {j.owner_kind === "plugin" && (
                    <Badge size="sm" variant="info">
                      plugin:{j.plugin_id}
                    </Badge>
                  )}
                </div>
                <div className="flex items-center gap-3 opacity-0 group-hover:opacity-100 transition-opacity">
                  <Button
                    variant="ghost"
                    size="xs"
                    loading={triggeringJobId === j.id}
                    onClick={() => triggerJob(j)}
                  >
                    {triggeringJobId === j.id ? "running…" : "run now"}
                  </Button>
                  <Button variant="ghost" size="xs" onClick={() => toggleRuns(j.id)}>
                    {expandedJobId === j.id ? "hide runs" : "show runs"}
                  </Button>
                  {j.owner_kind !== "plugin" ? (
                    <>
                      <Button variant="ghost" size="xs" onClick={() => toggleJob(j)}>
                        {j.enabled ? "disable" : "enable"}
                      </Button>
                      <Button variant="ghost" size="xs" onClick={() => editJob(j)}>
                        edit
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        className="text-destructive hover:text-destructive"
                        onClick={() =>
                          setConfirm({ msg: "Delete this job?", action: () => doDeleteJob(j.id) })
                        }
                      >
                        remove
                      </Button>
                    </>
                  ) : (
                    <span className="text-xs text-muted-foreground">managed by plugin</span>
                  )}
                </div>
              </div>

              <div className="text-sm text-muted-foreground mt-1">
                {j.owner_kind === "plugin"
                  ? j.description || "Plugin-owned scheduled job"
                  : j.message}
              </div>

              <div className="flex items-center gap-2 mt-2 flex-wrap">
                {j.owner_kind !== "plugin" && (
                  <Badge size="sm" variant="outline">
                    {j.session_mode}
                  </Badge>
                )}
                {j.agent_id && (
                  <Badge size="sm" variant="outline">
                    {j.agent_id}
                  </Badge>
                )}
                {j.owner_kind === "plugin" && (
                  <>
                    <Badge size="sm" variant="outline">
                      key:{j.job_key}
                    </Badge>
                    <Badge size="sm" variant="outline">
                      runtime:{j.runtime_name}
                    </Badge>
                  </>
                )}
                {isAdmin && j.owner_kind !== "plugin" && !j.user_id && (
                  <Badge size="sm" variant="secondary">
                    system
                  </Badge>
                )}
                {isAdmin && j.owner_kind !== "plugin" && !!j.user_id && (
                  <Badge size="sm" variant="outline">
                    user:{j.user_id}
                  </Badge>
                )}
                {j.last_run_at && (
                  <Badge size="sm" variant="outline">
                    last run: {formatTime(j.last_run_at)}
                  </Badge>
                )}
                {j.last_error && (
                  <Badge size="sm" variant="error">
                    error: {j.last_error}
                  </Badge>
                )}
              </div>

              {j.owner_kind === "plugin" && j.payload && Object.keys(j.payload).length > 0 && (
                <pre className="mt-3 text-xs bg-muted rounded-lg p-3 overflow-x-auto">
                  {JSON.stringify(j.payload, null, 2)}
                </pre>
              )}

              {expandedJobId === j.id && (
                <div className="mt-3 ml-4 border-l-2 border-border pl-4 space-y-2">
                  {!runHistories[j.id] || runHistories[j.id].length === 0 ? (
                    <p className="text-xs text-muted-foreground">No runs yet.</p>
                  ) : (
                    runHistories[j.id].map((run) => (
                      <div key={run.id} className="flex items-center gap-3 text-xs py-1">
                        <Badge size="sm" variant={statusBadgeVariant(run.status)}>
                          {run.status}
                        </Badge>
                        <span className="text-muted-foreground">{formatTime(run.started_at)}</span>
                        {run.duration && (
                          <span className="font-mono text-muted-foreground">{run.duration}</span>
                        )}
                        {run.session_id && (
                          <a
                            href={"/sessions/" + encodeURIComponent(run.session_id)}
                            className="text-primary hover:underline"
                          >
                            session
                          </a>
                        )}
                        {run.error && (
                          <span className="text-destructive truncate max-w-xs" title={run.error}>
                            {run.error}
                          </span>
                        )}
                      </div>
                    ))
                  )}
                </div>
              )}
            </div>
          ))}
        </div>

        {jobs.length === 0 && (
          <div className="py-12 text-center">
            <p className="text-sm text-muted-foreground">
              No jobs yet. Create one above to schedule recurring tasks.
            </p>
          </div>
        )}
      </div>

      {/* Confirm dialog */}
      <Dialog
        open={!!confirm}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
      >
        <DialogPopup className="max-w-sm">
          <DialogTitle>{confirm?.msg}</DialogTitle>
          <div className="flex justify-end gap-2 mt-4">
            <Button variant="outline" size="sm" onClick={() => setConfirm(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                confirm?.action();
                setConfirm(null);
              }}
            >
              Delete
            </Button>
          </div>
        </DialogPopup>
      </Dialog>

      {/* Toast */}
      {toast && (
        <div
          className={`fixed bottom-4 right-4 z-50 rounded-lg border px-4 py-3 text-sm shadow-md max-w-sm ${
            toast.kind === "error"
              ? "border-destructive/36 bg-destructive/8 text-destructive-foreground"
              : "border-success/36 bg-success/8 text-success-foreground"
          }`}
        >
          {toast.msg}
        </div>
      )}
    </div>
  );
}
