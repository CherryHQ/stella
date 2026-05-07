import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { formatTime } from "@/lib/time";
import type { Agent, SchedulerJob, SchedulerJobList, SchedulerJobRun } from "@/lib/types";

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

function statusBadgeClass(status: string): string {
  if (status === "success") return "badge-success";
  if (status === "error") return "badge-error";
  if (status === "running") return "badge-warning";
  return "badge-ghost";
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
    if (isAdmin && jobForm.system_job) {
      payload.user_id = 0;
    }
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

  const toggleJob = useCallback(async (j: SchedulerJob) => {
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
  }, [loadJobs, showToast]);

  const doDeleteJob = useCallback(async (id: number) => {
    const job = jobs.find((item) => item.id === id);
    if (job?.owner_kind === "plugin") return;
    try {
      await api("DELETE", "/api/scheduler/jobs/" + id);
      await loadJobs();
      showToast("Deleted");
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    }
  }, [jobs, loadJobs, showToast]);

  const triggerJob = useCallback(async (j: SchedulerJob) => {
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
  }, [expandedJobId, loadJobs, showToast]);

  const toggleRuns = useCallback(async (jobId: number) => {
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
  }, [expandedJobId]);

  const isFormValid =
    jobForm.name &&
    jobForm.message &&
    (jobForm.schedule_type === "cron" ? !!jobForm.cron : !!jobForm.every);

  return (
    <div>
      {/* Page header */}
      <div className="mb-8">
        <h1 className="font-serif text-2xl font-normal tracking-tight mb-1">Scheduled tasks</h1>
        <p className="text-sm text-secondary">Recurring jobs that Anna executes on a schedule.</p>
      </div>

      <div className="border-t border-base-300 pt-8">
        {/* Job form */}
        <div className="mb-8 pb-8 border-b border-base-300">
          <div className="flex items-center justify-between mb-4">
            <p className="text-xs font-mono font-medium text-secondary uppercase tracking-wider">
              {editingJobId ? "EDIT JOB" : "NEW JOB"}
            </p>
            {editingJobId && (
              <button onClick={resetForm} className="text-xs text-secondary hover:text-base-content cursor-pointer">
                Cancel
              </button>
            )}
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-8 gap-y-4 mb-4">
            <div>
              <label className="block text-xs font-mono text-secondary uppercase tracking-wider mb-1">Name</label>
              <input
                type="text"
                value={jobForm.name}
                onChange={(e) => setJobForm((f) => ({ ...f, name: e.target.value }))}
                placeholder="Daily summary"
                className="input input-bordered w-full text-sm"
              />
            </div>
            <div>
              <label className="block text-xs font-mono text-secondary uppercase tracking-wider mb-1">Session Mode</label>
              <select
                value={jobForm.session_mode}
                onChange={(e) => setJobForm((f) => ({ ...f, session_mode: e.target.value }))}
                className="select select-bordered w-full text-sm"
              >
                <option value="reuse">Reuse session</option>
                <option value="new">New session each run</option>
              </select>
            </div>
          </div>

          {/* Schedule */}
          <div className="mb-4">
            <label className="block text-xs font-mono text-secondary uppercase tracking-wider mb-1">Schedule</label>
            <div className="flex items-center gap-4 mb-2">
              <label className="flex items-center gap-2 text-sm cursor-pointer">
                <input
                  type="radio"
                  name="schedule_type"
                  value="cron"
                  checked={jobForm.schedule_type === "cron"}
                  onChange={() => setJobForm((f) => ({ ...f, schedule_type: "cron" }))}
                  className="radio radio-primary radio-sm"
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
                  className="radio radio-primary radio-sm"
                />
                <span>Interval</span>
              </label>
            </div>
            {jobForm.schedule_type === "cron" ? (
              <input
                type="text"
                value={jobForm.cron}
                onChange={(e) => setJobForm((f) => ({ ...f, cron: e.target.value }))}
                placeholder="0 9 * * 1-5"
                className="input input-bordered w-full text-sm font-mono"
              />
            ) : (
              <input
                type="text"
                value={jobForm.every}
                onChange={(e) => setJobForm((f) => ({ ...f, every: e.target.value }))}
                placeholder="30m, 2h"
                className="input input-bordered w-full text-sm font-mono"
              />
            )}
          </div>

          {/* Agent */}
          <div className="mb-4">
            <label className="block text-xs font-mono text-secondary uppercase tracking-wider mb-1">Agent</label>
            <select
              value={jobForm.agent_id}
              onChange={(e) => setJobForm((f) => ({ ...f, agent_id: e.target.value }))}
              className="select select-bordered w-full text-sm"
            >
              <option value="">Default agent</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>{a.name}</option>
              ))}
            </select>
          </div>

          {/* System job (admin only) */}
          {isAdmin && (
            <div className="mb-4">
              <label className="flex items-center gap-2 cursor-pointer text-sm">
                <input
                  type="checkbox"
                  checked={jobForm.system_job}
                  onChange={(e) => setJobForm((f) => ({ ...f, system_job: e.target.checked }))}
                  className="toggle toggle-secondary toggle-sm"
                />
                <span>System job</span>
                <span className="text-xs text-secondary">(broadcasts to all users)</span>
              </label>
            </div>
          )}

          {/* Message */}
          <div className="mb-4">
            <label className="block text-xs font-mono text-secondary uppercase tracking-wider mb-1">Message</label>
            <textarea
              value={jobForm.message}
              onChange={(e) => setJobForm((f) => ({ ...f, message: e.target.value }))}
              rows={2}
              placeholder="What should the agent do?"
              className="textarea textarea-bordered w-full text-sm resize-y"
            />
          </div>

          {/* Enabled + submit */}
          <div className="flex items-center justify-between">
            <label className="flex items-center gap-2 cursor-pointer text-sm">
              <input
                type="checkbox"
                checked={jobForm.enabled}
                onChange={(e) => setJobForm((f) => ({ ...f, enabled: e.target.checked }))}
                className="toggle toggle-primary toggle-sm"
              />
              <span>Enabled</span>
            </label>
            <button
              onClick={saveJob}
              disabled={!isFormValid}
              className="btn btn-primary btn-sm"
            >
              {editingJobId ? "Update" : "Create"}
            </button>
          </div>
        </div>

        {/* Job list */}
        <div className="divide-y divide-base-300">
          {jobs.map((j) => (
            <div key={j.id} className="py-5 group">
              <div className="flex items-baseline justify-between gap-4">
                <div className="flex items-baseline gap-3 flex-wrap">
                  <span className="font-medium">{j.name}</span>
                  <span className={`badge badge-sm ${j.enabled ? "badge-success" : "badge-ghost"}`}>
                    {j.enabled ? "on" : "off"}
                  </span>
                  <span className="text-xs font-mono text-secondary">{jobScheduleText(j)}</span>
                  {j.owner_kind === "plugin" && (
                    <span className="badge badge-info badge-xs">plugin:{j.plugin_id}</span>
                  )}
                </div>
                <div className="flex items-center gap-3 opacity-0 group-hover:opacity-100 transition-opacity">
                  <button
                    onClick={() => triggerJob(j)}
                    disabled={triggeringJobId === j.id}
                    className="text-xs text-secondary hover:text-primary cursor-pointer"
                  >
                    {triggeringJobId === j.id ? "running..." : "run now"}
                  </button>
                  <button
                    onClick={() => toggleRuns(j.id)}
                    className="text-xs text-secondary hover:text-base-content cursor-pointer"
                  >
                    {expandedJobId === j.id ? "hide runs" : "show runs"}
                  </button>
                  {j.owner_kind !== "plugin" ? (
                    <div className="flex items-center gap-3">
                      <button
                        onClick={() => toggleJob(j)}
                        className="text-xs text-secondary hover:text-base-content cursor-pointer"
                      >
                        {j.enabled ? "disable" : "enable"}
                      </button>
                      <button
                        onClick={() => editJob(j)}
                        className="text-xs text-secondary hover:text-primary cursor-pointer"
                      >
                        edit
                      </button>
                      <button
                        onClick={() => setConfirm({ msg: "Delete this job?", action: () => doDeleteJob(j.id) })}
                        className="text-xs text-secondary hover:text-error cursor-pointer"
                      >
                        remove
                      </button>
                    </div>
                  ) : (
                    <span className="text-xs text-secondary">managed by plugin</span>
                  )}
                </div>
              </div>

              <div className="text-sm text-secondary mt-1">
                {j.owner_kind === "plugin" ? (j.description || "Plugin-owned scheduled job") : j.message}
              </div>

              <div className="flex items-center gap-2 mt-2 flex-wrap">
                {j.owner_kind !== "plugin" && (
                  <span className="badge badge-ghost badge-xs">{j.session_mode}</span>
                )}
                {j.agent_id && (
                  <span className="badge badge-ghost badge-xs">{j.agent_id}</span>
                )}
                {j.owner_kind === "plugin" && (
                  <>
                    <span className="badge badge-ghost badge-xs">key:{j.job_key}</span>
                    <span className="badge badge-ghost badge-xs">runtime:{j.runtime_name}</span>
                  </>
                )}
                {isAdmin && j.owner_kind !== "plugin" && !j.user_id && (
                  <span className="badge badge-secondary badge-xs">system</span>
                )}
                {isAdmin && j.owner_kind !== "plugin" && !!j.user_id && (
                  <span className="badge badge-ghost badge-xs">user:{j.user_id}</span>
                )}
                {j.last_run_at && (
                  <span className="badge badge-ghost badge-xs">last run: {formatTime(j.last_run_at)}</span>
                )}
                {j.last_error && (
                  <span className="badge badge-error badge-xs">error: {j.last_error}</span>
                )}
              </div>

              {j.owner_kind === "plugin" && j.payload && Object.keys(j.payload).length > 0 && (
                <pre className="mt-3 text-xs bg-base-200 rounded-lg p-3 overflow-x-auto">
                  {JSON.stringify(j.payload, null, 2)}
                </pre>
              )}

              {/* Run history */}
              {expandedJobId === j.id && (
                <div className="mt-3 ml-4 border-l-2 border-base-300 pl-4 space-y-2">
                  {!runHistories[j.id] || runHistories[j.id].length === 0 ? (
                    <p className="text-xs text-secondary">No runs yet.</p>
                  ) : (
                    runHistories[j.id].map((run) => (
                      <div key={run.id} className="flex items-center gap-3 text-xs py-1">
                        <span className={`badge badge-xs ${statusBadgeClass(run.status)}`}>{run.status}</span>
                        <span className="text-secondary">{formatTime(run.started_at)}</span>
                        {run.duration && <span className="font-mono text-secondary">{run.duration}</span>}
                        {run.session_id && (
                          <a href={"/sessions/" + encodeURIComponent(run.session_id)} className="text-primary hover:underline">
                            session
                          </a>
                        )}
                        {run.error && (
                          <span className="text-error truncate max-w-xs" title={run.error}>{run.error}</span>
                        )}
                      </div>
                    ))
                  )}
                </div>
              )}
            </div>
          ))}
        </div>

        {/* Empty state */}
        {jobs.length === 0 && (
          <div className="py-12 text-center">
            <p className="text-sm text-secondary">No jobs yet. Create one above to schedule recurring tasks.</p>
          </div>
        )}
      </div>

      {/* Confirm dialog */}
      {confirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30">
          <div className="card bg-base-100 shadow-xl p-6 max-w-sm w-full">
            <p className="mb-4">{confirm.msg}</p>
            <div className="flex justify-end gap-2">
              <button onClick={() => setConfirm(null)} className="btn btn-ghost btn-sm">Cancel</button>
              <button
                onClick={() => { confirm.action(); setConfirm(null); }}
                className="btn btn-error btn-sm"
              >
                Delete
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Toast */}
      {toast && (
        <div className={`fixed bottom-4 right-4 z-50 alert ${toast.kind === "error" ? "alert-error" : "alert-success"} shadow-lg max-w-sm`}>
          <span className="text-sm">{toast.msg}</span>
        </div>
      )}
    </div>
  );
}
