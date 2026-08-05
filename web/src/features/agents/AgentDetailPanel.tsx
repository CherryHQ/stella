import { useCallback, useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  assignAgentUser,
  createAgent,
  deleteAgent,
  deleteAgentSkill,
  deleteAgentSkillFile,
  getAgentSkill,
  getAgentSkillFile,
  getBuiltinResource,
  installAgentSkill,
  listAgentSkills,
  listAgentUsers,
  listChannels,
  listProfileMemories,
  removeAgentUser,
  updateAgent,
  updateAgentSkill,
  updateChannel,
  uploadAgentSkill,
} from "@/lib/api-client/sdk.gen";
import type {
  InstallAgentSkillData,
  UpdateAgentData,
  UpdateAgentSkillData,
} from "@/lib/api-client/types.gen";
import type { BuiltinItem, Channel, Skill, User } from "@/lib/types";
import { apiErrorMessage } from "@/lib/api-error";
import {
  normalizeChannel,
  normalizeSandbox,
  type AgentsSettingsLoaderData,
} from "@/lib/queries/agent-settings";
import {
  agentRequestBody,
  initialAgentDetailState,
  type AgentsPageState,
} from "./agent-detail-state";
import { AgentForm, type AgentFormLayout } from "./AgentForm";
import { TemplateModal } from "./TemplateModal";
import { SkillInstallModal } from "./SkillInstallModal";
import { ConfirmDialog } from "./ConfirmDialog";
import { useToast, ToastContainer } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";

type ProfileMemory = { agent_id: string; soul?: string; content?: string };

// GET /api/users/me/memories wraps the list: { memories: [...] }.
function profileMemories(value: unknown) {
  const v = value as { memories?: ProfileMemory[] } | ProfileMemory[] | undefined;
  return (Array.isArray(v) ? v : v?.memories) ?? [];
}

export interface AgentDetailPanelProps {
  /** Bootstrap payload — a route loader's result or a fetched query. */
  data: AgentsSettingsLoaderData;
  /** Empty string means "create a new agent". */
  agentId: string;
  /** Dismissed without saving — also fired when the template picker is closed. */
  onClose?: () => void;
  onSaved?: (agentId: string) => void;
  onDeleted?: () => void;
  /** Sections a host surface already covers elsewhere (e.g. the profile's skills tab). */
  hiddenTabs?: readonly string[];
  layout?: AgentFormLayout;
}

/**
 * The agent editor: form state, save/delete, skill and user management, and the
 * modals they open. Hosts supply only routing and bootstrap data, so the
 * settings page and an agent's own profile page drive identical behavior.
 *
 * Remount (via `key`) when `agentId` changes — form drafts are intentionally
 * not resynced from `data`, so a stale mount would show the wrong agent.
 */
export function AgentDetailPanel({
  data,
  agentId,
  onClose,
  onSaved,
  onDeleted,
  hiddenTabs,
  layout = "page",
}: AgentDetailPanelProps) {
  const [state, setState] = useState<AgentsPageState>(() => initialAgentDetailState(data, agentId));
  const [creating] = useState(() => !agentId);
  const { toasts, showToast } = useToast();
  const { t } = useI18n();
  const queryClient = useQueryClient();

  const set = useCallback((patch: Partial<AgentsPageState>) => {
    setState((prev) => ({ ...prev, ...patch }));
  }, []);

  // Creating starts at the template picker whenever templates exist.
  useEffect(() => {
    if (!creating) return;
    setState((prev) =>
      prev.builtinTemplates.length > 0
        ? { ...prev, showTemplateModal: true, showForm: false }
        : { ...prev, showForm: true },
    );
  }, [creating]);

  const invalidateAgent = useCallback(
    (id: string) => {
      void queryClient.invalidateQueries({ queryKey: ["agents"] });
      void queryClient.invalidateQueries({ queryKey: ["agent-settings", id] });
    },
    [queryClient],
  );

  const loadChannels = useCallback(async () => {
    try {
      const { data: res } = await listChannels({ throwOnError: true });
      const channels = ((res?.channels ?? []) as Channel[]).map(normalizeChannel);
      setState((prev) => ({ ...prev, channels }));
      return channels;
    } catch {
      setState((prev) => ({ ...prev, channels: [] }));
      return [];
    }
  }, []);

  const loadAgentSkills = useCallback(async (id: string | null) => {
    if (!id) {
      setState((prev) => ({ ...prev, agentSkills: [] }));
      return [];
    }
    setState((prev) => ({ ...prev, agentSkillsLoading: true }));
    try {
      const { data: res } = await listAgentSkills({ path: { id }, throwOnError: true });
      const agentSkills = res?.skills ?? [];
      setState((prev) => ({
        ...prev,
        agentSkills: agentSkills as Skill[],
        agentSkillsLoading: false,
      }));
      return agentSkills;
    } catch {
      setState((prev) => ({ ...prev, agentSkills: [], agentSkillsLoading: false }));
      return [];
    }
  }, []);

  const loadPersonalisation = useCallback(async (id: string) => {
    if (!id) return;
    setState((prev) => ({
      ...prev,
      personalisation: { soul: "", soulDraft: "", profile: "", profileDraft: "", loaded: false },
    }));
    try {
      const { data: res } = await listProfileMemories({ throwOnError: true });
      const mem = profileMemories(res).find((m) => m.agent_id === id);
      const soul = mem?.soul ?? "";
      const profile = mem?.content ?? "";
      setState((prev) => ({
        ...prev,
        personalisation: { soul, soulDraft: soul, profile, profileDraft: profile, loaded: true },
      }));
    } catch {
      setState((prev) => ({
        ...prev,
        personalisation: { soul: "", soulDraft: "", profile: "", profileDraft: "", loaded: true },
      }));
    }
  }, []);

  const loadAssignedUsers = useCallback(async (id: string) => {
    try {
      const { data: res } = await listAgentUsers({ path: { id }, throwOnError: true });
      const assignedUsers = (res?.users ?? []).map(
        (u) => ({ id: u.id ?? "", email: u.username ?? "", name: "" }) as User,
      );
      setState((prev) => ({ ...prev, assignedUsers }));
    } catch {
      setState((prev) => ({ ...prev, assignedUsers: [] }));
    }
  }, []);

  // The user list used to load when its tab was opened; as a section it is
  // always one scroll away, so an admin editing an existing agent fetches it
  // up front. Non-admins never see the section and never pay for it.
  useEffect(() => {
    if (!agentId || !data.isAdmin) return;
    void loadAssignedUsers(agentId);
  }, [agentId, data.isAdmin, loadAssignedUsers]);

  const dedicatedChannelsForAgent = useCallback((id: string, channels: Channel[]) => {
    return channels.filter((ch) => ch.id !== ch.type && ch.agent_id === id);
  }, []);

  const saveChannelBindings = useCallback(
    async (agentID: string, currentState: AgentsPageState) => {
      if (!currentState.isAdmin) return;
      const selected = new Set(currentState.selectedChannelIDs);
      const available = currentState.channels.filter(
        (ch) => ch.id !== ch.type && ch.enabled && (!ch.agent_id || ch.agent_id === agentID),
      );
      for (const ch of available) {
        const wantsAgent = selected.has(ch.id);
        const nextAgentID = wantsAgent ? agentID : "";
        if ((ch.agent_id || "") === nextAgentID) continue;
        await updateChannel({
          path: { id: ch.id },
          body: {
            type: ch.type,
            agent_id: nextAgentID,
            config: JSON.stringify(ch._config || {}),
          },
          throwOnError: true,
        });
      }
      await loadChannels();
    },
    [loadChannels],
  );

  const saveAgent = useCallback(
    async (currentState: AgentsPageState) => {
      try {
        const payload = {
          ...currentState.form,
          sandbox: normalizeSandbox(currentState.form.sandbox),
        };
        if (payload.sandbox.network.mode !== "whitelist") {
          payload.sandbox.network.allowlist = [];
        }

        if (currentState.editingId) {
          await updateAgent({
            path: { id: currentState.editingId },
            body: agentRequestBody(payload) as UpdateAgentData["body"],
            throwOnError: true,
          });
          await saveChannelBindings(currentState.editingId, currentState);
          invalidateAgent(currentState.editingId);
          showToast(t("common.saved"));
          onSaved?.(currentState.editingId);
        } else {
          const { data: created } = await createAgent({
            body: agentRequestBody(payload),
            throwOnError: true,
          });
          const newId = created!.id!;
          setState((prev) => ({ ...prev, editingId: newId }));
          await Promise.all([
            saveChannelBindings(newId, { ...currentState, editingId: newId }),
            loadAgentSkills(newId),
            loadPersonalisation(newId),
          ]);
          const channels = await loadChannels();
          const selectedChannelIDs = dedicatedChannelsForAgent(newId, channels).map((c) => c.id);
          setState((prev) => ({ ...prev, selectedChannelIDs }));
          invalidateAgent(newId);
          showToast(t("common.saved"));
          onSaved?.(newId);
        }
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [
      saveChannelBindings,
      loadChannels,
      loadAgentSkills,
      loadPersonalisation,
      dedicatedChannelsForAgent,
      invalidateAgent,
      showToast,
      onSaved,
      t,
    ],
  );

  const doDeleteAgent = useCallback(
    async (id: string) => {
      try {
        await deleteAgent({ path: { id }, throwOnError: true });
        invalidateAgent(id);
        showToast(t("common.deleted"));
        onDeleted?.();
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [invalidateAgent, showToast, onDeleted, t],
  );

  const addUser = useCallback(
    async (currentState: AgentsPageState) => {
      if (!currentState.addUserId || !currentState.editingId) return;
      try {
        await assignAgentUser({
          path: { id: currentState.editingId },
          body: { user_id: currentState.addUserId },
          throwOnError: true,
        });
        setState((prev) => ({ ...prev, addUserId: "" }));
        await loadAssignedUsers(currentState.editingId);
        showToast(t("agents.detail.userAssigned"));
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAssignedUsers, showToast, t],
  );

  const removeUser = useCallback(
    async (userId: string, editingId: string) => {
      try {
        await removeAgentUser({ path: { id: editingId, userId }, throwOnError: true });
        await loadAssignedUsers(editingId);
        showToast(t("agents.detail.userRemoved"));
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAssignedUsers, showToast, t],
  );

  const skillKey = (sk: { scope: string; id: string }) => `${sk.scope}:${sk.id}`;

  const selectSkillFile = useCallback(
    async (
      path: string,
      skill: Skill,
      fileCache: Record<string, { content: string; encoding?: string }>,
      editingId: string | null,
      skipDirtyCheck = false,
      isDirty = false,
    ) => {
      if (!skill || !path) return;
      if (!skipDirtyCheck && isDirty && !confirm(t("agents.detail.discardChanges"))) return;
      // Drop the previous file's content and mark the new one unloaded before
      // fetching, so a failed fetch can never leave stale text attributed to
      // the newly active file.
      setState((prev) => ({
        ...prev,
        selectedSkillActiveFile: path,
        selectedSkillFileContent: "",
        selectedSkillFileEncoding: "",
        selectedSkillFileLoaded: false,
      }));
      if (Object.prototype.hasOwnProperty.call(fileCache, path)) {
        setState((prev) => ({
          ...prev,
          selectedSkillFileContent: fileCache[path].content,
          selectedSkillFileEncoding: fileCache[path].encoding ?? "",
          selectedSkillFileLoaded: true,
          selectedSkillDirty: false,
        }));
        return;
      }
      setState((prev) => ({ ...prev, selectedSkillFileLoading: true }));
      try {
        const { data: res } = await getAgentSkillFile({
          path: { id: editingId ?? "", skillId: skill.name },
          query: { path, scope: skill.scope as UpdateAgentSkillData["query"]["scope"] },
          throwOnError: true,
        });
        const file = res as { content?: string; encoding?: string } | undefined;
        const content = file?.content ?? "";
        const encoding = file?.encoding ?? "";
        setState((prev) => ({
          ...prev,
          selectedSkillFileContent: content,
          selectedSkillFileEncoding: encoding,
          selectedSkillFileLoaded: true,
          selectedSkillFileCache: {
            ...prev.selectedSkillFileCache,
            [path]: { content, encoding: encoding || undefined },
          },
          selectedSkillDirty: false,
          selectedSkillFileLoading: false,
        }));
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
        setState((prev) => ({ ...prev, selectedSkillFileLoading: false }));
      }
    },
    [showToast, t],
  );

  const selectSkill = useCallback(
    async (sk: Skill, currentState: AgentsPageState) => {
      if (!sk) return;
      if (currentState.selectedSkillDirty && !confirm(t("agents.detail.discardChanges"))) return;
      const key = skillKey(sk);
      setState((prev) => ({
        ...prev,
        selectedSkillKey: key,
        selectedSkillLoading: true,
        selectedSkill: null,
        selectedSkillDirty: false,
        selectedSkillEditMode: false,
        selectedSkillShowAdvanced: false,
        selectedSkillFileCache: {},
        selectedSkillAddingFile: false,
        selectedSkillNewFileName: "",
      }));
      try {
        const { data: raw } = await getAgentSkill({
          path: { id: currentState.editingId ?? "", skillId: sk.name },
          query: { scope: sk.scope as UpdateAgentSkillData["query"]["scope"] },
          throwOnError: true,
        });
        const unwrapped = raw as Skill;
        const skill: Skill = { ...unwrapped, scope: sk.scope };
        const files = unwrapped.files ?? ["SKILL.md"];
        const initialFile = files.includes("SKILL.md") ? "SKILL.md" : files[0];
        setState((prev) => ({ ...prev, selectedSkill: skill, selectedSkillLoading: false }));
        await selectSkillFile(initialFile, skill, {}, currentState.editingId, true, false);
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
        setState((prev) => ({ ...prev, selectedSkillLoading: false }));
      }
    },
    [selectSkillFile, showToast, t],
  );

  const saveSelectedSkill = useCallback(
    async (currentState: AgentsPageState) => {
      const { selectedSkill, selectedSkillFileContent, selectedSkillActiveFile } = currentState;
      if (!selectedSkill || selectedSkill.scope === "system") return;
      // Hard gate at the mutation boundary: only write the active file back if
      // its content was successfully loaded and it is not binary. An unloaded
      // or failed fetch would overwrite the file with empty/stale text, and a
      // binary file's base64 transport form would corrupt it.
      const activeFileEditable =
        currentState.selectedSkillFileLoaded && currentState.selectedSkillFileEncoding !== "base64";
      setState((prev) => ({ ...prev, selectedSkillSaving: true }));
      try {
        await updateAgentSkill({
          path: { id: currentState.editingId ?? "", skillId: selectedSkill.name },
          query: { scope: selectedSkill.scope as UpdateAgentSkillData["query"]["scope"] },
          body: {
            description: selectedSkill.description,
            disable_model_invocation: !!selectedSkill.disable_model_invocation,
            ...(activeFileEditable
              ? { files: { [selectedSkillActiveFile]: selectedSkillFileContent } }
              : {}),
          } as UpdateAgentSkillData["body"],
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          selectedSkillDirty: false,
          selectedSkillFileCache: activeFileEditable
            ? {
                ...prev.selectedSkillFileCache,
                [selectedSkillActiveFile]: { content: selectedSkillFileContent },
              }
            : prev.selectedSkillFileCache,
        }));
        const { data: raw2 } = await getAgentSkill({
          path: { id: currentState.editingId ?? "", skillId: selectedSkill.name },
          query: { scope: selectedSkill.scope as UpdateAgentSkillData["query"]["scope"] },
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          selectedSkill: { ...(raw2 as Skill), scope: selectedSkill.scope },
          selectedSkillSaving: false,
        }));
        await loadAgentSkills(currentState.editingId);
        showToast(t("common.saved"));
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
        setState((prev) => ({ ...prev, selectedSkillSaving: false }));
      }
    },
    [loadAgentSkills, showToast, t],
  );

  const deleteSkill = useCallback(
    async (sk: Skill, currentState: AgentsPageState) => {
      if (sk.scope === "system") return;
      if (!confirm(t("agents.detail.confirmDeleteSkill", { name: sk.name }))) return;
      try {
        await deleteAgentSkill({
          path: { id: currentState.editingId ?? "", skillId: sk.name },
          query: { scope: sk.scope as UpdateAgentSkillData["query"]["scope"] },
          throwOnError: true,
        });
        setState((prev) => {
          const wasSelected = prev.selectedSkillKey === skillKey(sk);
          return {
            ...prev,
            selectedSkillKey: wasSelected ? "" : prev.selectedSkillKey,
            selectedSkill: wasSelected ? null : prev.selectedSkill,
            selectedSkillDirty: wasSelected ? false : prev.selectedSkillDirty,
          };
        });
        await loadAgentSkills(currentState.editingId);
        showToast(t("agents.detail.skillRemoved"));
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAgentSkills, showToast, t],
  );

  const doSkillInstall = useCallback(
    async (source: string, scope: "user_agent" | "system_agent", currentState: AgentsPageState) => {
      if (!source) {
        showToast(t("agents.detail.chooseSkillFirst"), "error");
        return;
      }
      if (!currentState.editingId) return;
      try {
        const { data: res } = await installAgentSkill({
          path: { id: currentState.editingId },
          body: { source, scope } as InstallAgentSkillData["body"],
          throwOnError: true,
        });
        showToast(t("agents.detail.skillInstalled", { name: res?.name ?? "skill" }));
        setState((prev) => ({ ...prev, skillInstallModalOpen: false }));
        const updated = await loadAgentSkills(currentState.editingId);
        const created = updated.find((sk) => sk.name === (res?.name ?? ""));
        if (created) {
          await selectSkill({ ...created, scope } as Skill, {
            ...currentState,
            agentSkills: updated as Skill[],
          });
        }
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAgentSkills, selectSkill, showToast, t],
  );

  const doSkillUpload = useCallback(
    async (file: File, scope: "user_agent" | "system_agent", currentState: AgentsPageState) => {
      if (!file) {
        showToast(t("agents.detail.chooseZipFirst"), "error");
        return;
      }
      if (!currentState.editingId) return;
      try {
        const { data: res } = await uploadAgentSkill({
          path: { id: currentState.editingId },
          body: { file, scope },
          throwOnError: true,
        });
        showToast(t("agents.detail.skillUploaded", { name: res?.name ?? "skill" }));
        setState((prev) => ({ ...prev, skillInstallModalOpen: false }));
        const updated = await loadAgentSkills(currentState.editingId);
        const created = updated.find((sk) => sk.id === res?.id);
        if (created) {
          await selectSkill({ ...created, scope } as Skill, {
            ...currentState,
            agentSkills: updated as Skill[],
          });
        }
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAgentSkills, selectSkill, showToast, t],
  );

  const deleteSelectedSkillFile = useCallback(
    async (currentState: AgentsPageState) => {
      const { selectedSkill, selectedSkillActiveFile, editingId } = currentState;
      if (!selectedSkill || selectedSkill.scope === "system") return;
      if (!confirm(t("agents.detail.confirmDeleteFile", { name: selectedSkillActiveFile }))) return;
      try {
        await deleteAgentSkillFile({
          path: { id: editingId ?? "", skillId: selectedSkill.name },
          query: {
            path: selectedSkillActiveFile,
            scope: selectedSkill.scope as UpdateAgentSkillData["query"]["scope"],
          },
          throwOnError: true,
        });
        const newFiles = (selectedSkill.files ?? ["SKILL.md"]).filter(
          (f) => f !== selectedSkillActiveFile,
        );
        setState((prev) => ({
          ...prev,
          selectedSkill: { ...selectedSkill, files: newFiles },
          selectedSkillActiveFile: "SKILL.md",
          selectedSkillFileCache: Object.fromEntries(
            Object.entries(prev.selectedSkillFileCache).filter(
              ([k]) => k !== selectedSkillActiveFile,
            ),
          ),
        }));
        await selectSkillFile(
          "SKILL.md",
          { ...selectedSkill, files: newFiles },
          currentState.selectedSkillFileCache,
          editingId,
          true,
          false,
        );
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [selectSkillFile, showToast, t],
  );

  const applySoul = useCallback(
    async (soulID: string) => {
      if (!soulID) return;
      try {
        const { data: full } = await getBuiltinResource({
          path: { kind: "soul", id: soulID },
          throwOnError: true,
        });
        setState((prev) => ({ ...prev, form: { ...prev.form, soul: full?.content ?? "" } }));
      } catch (e) {
        setState((prev) => ({ ...prev, selectedSoulID: "" }));
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [showToast, t],
  );

  const pickTemplate = useCallback(
    async (tmpl: BuiltinItem) => {
      try {
        const { data: full } = await getBuiltinResource({
          path: { kind: "template", id: tmpl.id },
          throwOnError: true,
        });
        const meta = (full?.metadata ?? {}) as Record<string, string>;
        let soulContent = "";
        if (meta.soul_id) {
          try {
            const { data: soul } = await getBuiltinResource({
              path: { kind: "soul", id: meta.soul_id },
              throwOnError: true,
            });
            soulContent = soul?.content ?? "";
          } catch {}
        }
        setState((prev) => ({
          ...prev,
          form: {
            ...prev.form,
            name: prev.form.name || tmpl.name || "",
            model: meta.model || prev.form.model || "",
            system_prompt: full?.content ?? "",
            soul: soulContent,
            template_id: tmpl.id,
          },
          showTemplateModal: false,
          showForm: true,
        }));
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [showToast, t],
  );

  return (
    <>
      {state.showForm && (
        <AgentForm
          state={state}
          layout={layout}
          hiddenTabs={hiddenTabs}
          onSetState={set}
          onSave={() => saveAgent(state)}
          onCancel={onClose}
          onAddUser={() => addUser(state)}
          onRemoveUser={(userId) => removeUser(userId, state.editingId ?? "")}
          onApplySoul={applySoul}
          onSelectSkill={(sk) => selectSkill(sk, state)}
          onSaveSelectedSkill={() => saveSelectedSkill(state)}
          onDeleteSkill={(sk) => deleteSkill(sk, state)}
          onSelectSkillFile={(path, skipDirtyCheck) =>
            selectSkillFile(
              path,
              state.selectedSkill!,
              state.selectedSkillFileCache,
              state.editingId,
              skipDirtyCheck,
              state.selectedSkillDirty,
            )
          }
          onDeleteSkillFile={() => deleteSelectedSkillFile(state)}
          onOpenSkillInstallModal={(scope) =>
            setState((prev) => ({
              ...prev,
              skillInstallModalOpen: true,
              skillInstallScope:
                scope ?? (prev.isAdmin && prev.editingId ? "system_agent" : "user_agent"),
            }))
          }
          onDelete={state.editingId ? () => doDeleteAgent(state.editingId!) : undefined}
        />
      )}
      {state.showTemplateModal && (
        <TemplateModal
          templates={state.builtinTemplates}
          onPick={pickTemplate}
          onPickBlank={() =>
            setState((prev) => ({
              ...prev,
              showTemplateModal: false,
              showForm: true,
            }))
          }
          onClose={() => {
            setState((prev) => ({ ...prev, showTemplateModal: false }));
            onClose?.();
          }}
        />
      )}
      {state.skillInstallModalOpen && (
        <SkillInstallModal
          state={state}
          onClose={() => setState((prev) => ({ ...prev, skillInstallModalOpen: false }))}
          onSetScope={(scope) => setState((prev) => ({ ...prev, skillInstallScope: scope }))}
          onInstall={(source, scope) => doSkillInstall(source, scope, state)}
          onUpload={(file, scope) => doSkillUpload(file, scope, state)}
          showToast={showToast}
        />
      )}
      {state.confirmMsg && (
        <ConfirmDialog
          message={state.confirmMsg}
          onConfirm={() => {
            state.confirmAction();
            setState((prev) => ({ ...prev, confirmMsg: "" }));
          }}
          onCancel={() => setState((prev) => ({ ...prev, confirmMsg: "" }))}
        />
      )}
      <ToastContainer messages={toasts} />
    </>
  );
}
