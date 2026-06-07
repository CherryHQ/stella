import { useCallback, useEffect, useRef, useState } from "react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { useQuery } from "@tanstack/react-query";
import {
  createSchedulerJob,
  deleteSchedulerJob,
  listAgents,
  listSchedulerJobs,
  triggerSchedulerJob,
  updateSchedulerJob,
} from "@/lib/api-client/sdk.gen";
import { fetchAllSchedulerJobRuns } from "@/lib/paginated";
import { meQueryOptions } from "@/lib/queries/me";
import { formatTime } from "@/lib/time";
import type { ComponentsJobInput } from "@/lib/api-client/types.gen";
import type { Agent, SchedulerJob, SchedulerJobRun } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";

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
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;
  const [jobs, setJobs] = useState<SchedulerJob[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const agentsRef = useRef<Agent[]>([]);
  const [selectedJobId, setSelectedJobId] = useState<string | null>(null);
  const [editingJobId, setEditingJobId] = useState<string | null>(null);
  const [creatingNew, setCreatingNew] = useState(false);
  const [triggeringJobId, setTriggeringJobId] = useState<string | null>(null);
  const [runHistories, setRunHistories] = useState<Record<string, SchedulerJobRun[]>>({});
  const [jobForm, setJobForm] = useState<JobForm>(emptyForm());
  const [toast, setToast] = useState<Toast | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState | null>(null);

  const showToast = useCallback((msg: string, kind: "success" | "error" = "success") => {
    setToast({ msg, kind });
    setTimeout(() => setToast(null), 3000);
  }, []);

  const loadJobs = useCallback(async () => {
    try {
      let agentList = agentsRef.current;
      if (agentList.length === 0) {
        const { data } = await listAgents({ throwOnError: true });
        agentList = (data?.agents ?? []) as Agent[];
        agentsRef.current = agentList;
        setAgents(agentList);
      }
      const lists = await Promise.all(
        agentList.map((agent) =>
          listSchedulerJobs({ path: { agentId: agent.id }, throwOnError: true })
            .then(({ data }) => ({
              items: ((data?.jobs ?? []) as SchedulerJob[]).map((job) => ({
                ...job,
                agent_id: job.agent_id || agent.id,
              })),
            }))
            .catch(() => ({ items: [] as SchedulerJob[] })),
        ),
      );
      setJobs(lists.flatMap((list) => list.items || []));
    } catch (e) {
      console.error(e);
    }
  }, []);

  const loadAgents = useCallback(async () => {
    try {
      const { data } = await listAgents({ throwOnError: true });
      const list = (data?.agents ?? []) as Agent[];
      agentsRef.current = list;
      setAgents(list);
    } catch (e) {
      console.error(e);
    }
  }, []);

  useEffect(() => {
    void Promise.all([loadJobs(), loadAgents()]);
  }, [loadJobs, loadAgents]);

  const loadRuns = useCallback(
    async (jobId: string) => {
      try {
        const job = jobs.find((item) => item.id === jobId);
        if (!job?.agent_id) return;
        const runs = await fetchAllSchedulerJobRuns(job.agent_id, jobId);
        setRunHistories((prev) => ({ ...prev, [jobId]: runs as SchedulerJobRun[] }));
      } catch (e) {
        console.error(e);
      }
    },
    [jobs],
  );

  useEffect(() => {
    if (selectedJobId !== null) {
      void loadRuns(selectedJobId);
    }
  }, [selectedJobId, loadRuns]);

  const resetForm = useCallback(() => {
    setJobForm(emptyForm());
    setEditingJobId(null);
  }, []);

  const selectJob = useCallback(
    (j: SchedulerJob) => {
      setSelectedJobId(j.id);
      setCreatingNew(false);
      if (j.owner_kind === "user") {
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
      } else {
        resetForm();
      }
    },
    [resetForm],
  );

  const startNew = useCallback(() => {
    setSelectedJobId(null);
    setCreatingNew(true);
    resetForm();
  }, [resetForm]);

  const saveJob = useCallback(async () => {
    const payload: ComponentsJobInput = {
      name: jobForm.name,
      message: jobForm.message,
      cron: jobForm.schedule_type === "cron" ? jobForm.cron : "",
      every: jobForm.schedule_type === "every" ? jobForm.every : "",
      session_mode: jobForm.session_mode,
      enabled: jobForm.enabled,
      agent_id: jobForm.agent_id,
    };
    if (isAdmin && jobForm.system_job) payload.user_id = "";
    try {
      if (editingJobId !== null) {
        await updateSchedulerJob({
          path: { agentId: jobForm.agent_id, jobId: editingJobId },
          body: payload,
          throwOnError: true,
        });
      } else {
        await createSchedulerJob({
          path: { agentId: jobForm.agent_id },
          body: payload,
          throwOnError: true,
        });
      }
      resetForm();
      setSelectedJobId(null);
      setCreatingNew(false);
      await loadJobs();
      showToast("Saved");
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Request failed", "error");
    }
  }, [jobForm, isAdmin, editingJobId, resetForm, loadJobs, showToast]);

  const doDeleteJob = useCallback(
    async (id: string) => {
      const job = jobs.find((item) => item.id === id);
      if (job?.owner_kind !== "user") return;
      try {
        await deleteSchedulerJob({
          path: { agentId: job.agent_id ?? "", jobId: id },
          throwOnError: true,
        });
        if (selectedJobId === id) {
          setSelectedJobId(null);
          resetForm();
        }
        await loadJobs();
        showToast("Deleted");
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      }
    },
    [jobs, selectedJobId, resetForm, loadJobs, showToast],
  );

  const triggerJob = useCallback(
    async (j: SchedulerJob) => {
      setTriggeringJobId(j.id);
      try {
        await triggerSchedulerJob({
          path: { agentId: j.agent_id ?? "", jobId: j.id },
          throwOnError: true,
        });
        showToast("Job triggered");
        if (selectedJobId === j.id) {
          void loadRuns(j.id);
        }
        await loadJobs();
      } catch (e) {
        showToast(e instanceof Error ? e.message : "Request failed", "error");
      } finally {
        setTriggeringJobId(null);
      }
    },
    [selectedJobId, loadRuns, loadJobs, showToast],
  );

  const isFormValid =
    jobForm.name &&
    jobForm.message &&
    (jobForm.schedule_type === "cron" ? !!jobForm.cron : !!jobForm.every);

  const selectedJob = selectedJobId !== null ? jobs.find((j) => j.id === selectedJobId) : null;
  const runs = selectedJobId !== null ? runHistories[selectedJobId] : undefined;

  return (
    <div className="flex overflow-hidden" style={{ height: "calc(100vh - 3.5rem)" }}>
      {/* Left panel: job list */}
      <div className="w-[320px] min-w-[320px] shrink-0 border-r border-border bg-background flex flex-col overflow-hidden">
        <div className="px-4 py-3 border-b border-border flex items-center justify-between">
          <span className="text-xs font-semibold text-muted-foreground">{t("scheduler.jobs")}</span>
          <Button size="xs" onClick={startNew}>
            + New
          </Button>
        </div>
        <div className="flex-1 overflow-y-auto">
          {jobs.map((j) => (
            <div
              key={`${j.agent_id ?? ""}:${j.id}`}
              onClick={() => selectJob(j)}
              className={`px-4 py-3 border-b border-border cursor-pointer transition-colors border-l-2 ${
                selectedJobId === j.id
                  ? "border-l-primary bg-primary/[0.03]"
                  : "border-l-transparent hover:bg-muted"
              }`}
            >
              <div className="flex items-center justify-between gap-2 mb-1">
                <span className="text-[13px] font-medium truncate">{j.name}</span>
                <span className="text-[10px] font-mono text-muted-foreground shrink-0">
                  {jobScheduleText(j)}
                </span>
              </div>
              <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <Badge size="sm" variant={j.enabled ? "success" : "outline"}>
                  {j.enabled ? "on" : "off"}
                </Badge>
                {j.owner_kind === "plugin" ? (
                  <Badge size="sm" variant="info">
                    plugin:{j.plugin_id}
                  </Badge>
                ) : j.owner_kind === "system" ? (
                  <Badge size="sm" variant="secondary">
                    system
                  </Badge>
                ) : (
                  <>
                    {j.agent_id && <span>{j.agent_id}</span>}
                    <span className="text-muted-foreground">{j.session_mode}</span>
                  </>
                )}
              </div>
            </div>
          ))}
          {jobs.length === 0 && (
            <div className="py-12 text-center">
              <p className="text-sm text-muted-foreground">No jobs yet.</p>
            </div>
          )}
        </div>
      </div>

      {/* Right panel: detail / form */}
      <div className="flex-1 overflow-y-auto p-8 px-10">
        {selectedJob ? (
          <>
            <div className="flex items-center justify-between mb-6">
              <h2 className="font-serif text-xl tracking-tight">{selectedJob.name}</h2>
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  loading={triggeringJobId === selectedJob.id}
                  onClick={() => triggerJob(selectedJob)}
                >
                  {t("scheduler.runNow")}
                </Button>
                {selectedJob.owner_kind === "user" && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-destructive hover:text-destructive"
                    onClick={() =>
                      setConfirm({
                        msg: "Delete this job?",
                        action: () => doDeleteJob(selectedJob.id),
                      })
                    }
                  >
                    {t("common.delete")}
                  </Button>
                )}
              </div>
            </div>

            {selectedJob.owner_kind === "user" ? (
              <>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-4 mb-4">
                  <div className="space-y-1.5">
                    <label className="block text-xs font-semibold text-muted-foreground">
                      Name
                    </label>
                    <Input
                      type="text"
                      value={jobForm.name}
                      onChange={(e) => setJobForm((f) => ({ ...f, name: e.target.value }))}
                      placeholder={t("scheduler.dailySummary")}
                      nativeInput
                    />
                  </div>
                  <div className="space-y-1.5">
                    <label className="block text-xs font-semibold text-muted-foreground">
                      Session Mode
                    </label>
                    <select
                      value={jobForm.session_mode}
                      onChange={(e) => setJobForm((f) => ({ ...f, session_mode: e.target.value }))}
                      className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
                    >
                      <option value="reuse">{t("scheduler.reuseSession")}</option>
                      <option value="new">{t("scheduler.newSessionEachRun")}</option>
                    </select>
                  </div>
                </div>

                <div className="mb-4 space-y-1.5">
                  <label className="block text-xs font-semibold text-muted-foreground">
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
                      <span>{t("scheduler.cron2")}</span>
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
                  <label className="block text-xs font-semibold text-muted-foreground">Agent</label>
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
                  <label className="block text-xs font-semibold text-muted-foreground">
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
                    <span className="text-sm">{t("scheduler.enabled")}</span>
                  </div>
                </div>

                <div className="flex items-center gap-3 mt-5 pt-5 border-t border-border">
                  <Button size="sm" disabled={!isFormValid} onClick={saveJob}>
                    {t("common.save")}
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => selectJob(selectedJob)}>
                    {t("common.cancel")}
                  </Button>
                </div>
              </>
            ) : (
              <div className="space-y-3">
                <MarkdownPreview
                  content={selectedJob.description || selectedJob.message || ""}
                  className="[&_ol]:pl-5 [&_ul]:pl-5"
                />
                <div className="flex items-center gap-2 flex-wrap">
                  {selectedJob.owner_kind === "plugin" ? (
                    <Badge size="sm" variant="info">
                      plugin:{selectedJob.plugin_id}
                    </Badge>
                  ) : (
                    <Badge size="sm" variant="secondary">
                      system
                    </Badge>
                  )}
                  {selectedJob.owner_kind === "plugin" && selectedJob.job_key && (
                    <Badge size="sm" variant="outline">
                      key:{selectedJob.job_key}
                    </Badge>
                  )}
                  {selectedJob.owner_kind === "plugin" && selectedJob.runtime_name && (
                    <Badge size="sm" variant="outline">
                      runtime:{selectedJob.runtime_name}
                    </Badge>
                  )}
                  <Badge size="sm" variant={selectedJob.enabled ? "success" : "outline"}>
                    {selectedJob.enabled ? "on" : "off"}
                  </Badge>
                </div>
                {selectedJob.payload && Object.keys(selectedJob.payload).length > 0 && (
                  <pre className="text-xs bg-muted rounded-lg p-3 overflow-x-auto">
                    {JSON.stringify(selectedJob.payload, null, 2)}
                  </pre>
                )}
              </div>
            )}

            {/* Run history */}
            <div className="mt-8 pt-6 border-t border-border">
              <h3 className="text-[13px] font-semibold mb-3">Recent runs</h3>
              {!runs || runs.length === 0 ? (
                <p className="text-xs text-muted-foreground">No runs yet.</p>
              ) : (
                <div className="space-y-0">
                  {runs.map((run) => (
                    <div
                      key={run.id}
                      className="flex items-center gap-3 text-xs py-2 border-b border-border"
                    >
                      <Badge size="sm" variant={statusBadgeVariant(run.status)}>
                        {run.status}
                      </Badge>
                      <span className="font-mono text-[11px] text-muted-foreground">
                        {formatTime(run.started_at)}
                      </span>
                      {run.duration && (
                        <span className="font-mono text-[11px] text-muted-foreground">
                          {run.duration}
                        </span>
                      )}
                      {run.session_id && (
                        <a
                          href={"/sessions/" + encodeURIComponent(run.session_id)}
                          className="text-primary hover:underline text-[11px]"
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
                  ))}
                </div>
              )}
            </div>
          </>
        ) : creatingNew ? (
          <div>
            <h2 className="font-serif text-xl tracking-tight mb-6">New job</h2>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-4 mb-4">
              <div className="space-y-1.5">
                <label className="block text-xs font-semibold text-muted-foreground">Name</label>
                <Input
                  type="text"
                  value={jobForm.name}
                  onChange={(e) => setJobForm((f) => ({ ...f, name: e.target.value }))}
                  placeholder={t("scheduler.dailySummary")}
                  nativeInput
                />
              </div>
              <div className="space-y-1.5">
                <label className="block text-xs font-semibold text-muted-foreground">
                  Session Mode
                </label>
                <select
                  value={jobForm.session_mode}
                  onChange={(e) => setJobForm((f) => ({ ...f, session_mode: e.target.value }))}
                  className="h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
                >
                  <option value="reuse">{t("scheduler.reuseSession")}</option>
                  <option value="new">{t("scheduler.newSessionEachRun")}</option>
                </select>
              </div>
            </div>

            <div className="mb-4 space-y-1.5">
              <label className="block text-xs font-semibold text-muted-foreground">Schedule</label>
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
                  <span>{t("scheduler.cron2")}</span>
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
              <label className="block text-xs font-semibold text-muted-foreground">Agent</label>
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
              <label className="block text-xs font-semibold text-muted-foreground">Message</label>
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
                <span className="text-sm">{t("scheduler.enabled")}</span>
              </div>
              <Button size="sm" disabled={!isFormValid} onClick={saveJob}>
                {t("common.create")}
              </Button>
            </div>
          </div>
        ) : (
          <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
            Select a job or create a new one
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
              {t("common.cancel")}
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                confirm?.action();
                setConfirm(null);
              }}
            >
              {t("common.delete")}
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
