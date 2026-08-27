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
  listProfileMemories,
  removeAgentUser,
  updateAgent,
  updateAgentSkillActivation,
  updateAgentSkill,
  uploadAgentSkill,
} from "@/lib/api-client/sdk.gen";
import type {
  InstallAgentSkillData,
  UpdateAgentData,
  UpdateAgentSkillData,
} from "@/lib/api-client/types.gen";
import type { BuiltinItem, Skill, User } from "@/lib/types";
import { apiErrorMessage } from "@/lib/api-error";
import { toSkillScope } from "@/lib/skill-scope";
import { normalizeSandbox, type AgentsSettingsLoaderData } from "@/lib/queries/agent-settings";
import {
  agentRequestBody,
  initialAgentDetailState,
  type AgentsPageState,
} from "./agent-detail-state";
import { AgentForm, type AgentFormLayout } from "./AgentForm";
import { TemplateModal } from "./TemplateModal";
import { SkillInstallModal } from "./SkillInstallModal";
import { ConfirmDialog } from "./ConfirmDialog";
import { useToast } from "@/hooks/use-toast";
import { useI18n } from "@/lib/i18n";

type ProfileMemory = { agent_id?: string; soul?: string; content?: string };
type ProfileMemoryPayload = ProfileMemory[] | { memories?: ProfileMemory[] } | undefined;

// GET /api/users/me/memories wraps the list: { memories: [...] }.
function profileMemories(value: ProfileMemoryPayload) {
  // SAFETY: the API returns either the wrapper object or the bare array; both carry the list.
  const v = value;
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
  const { showToast } = useToast();
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

  const loadAgentSkills = useCallback(async (id: string | null) => {
    if (!id) {
      setState((prev) => ({
        ...prev,
        agentSkills: [],
        agentSkillCanManageActivation: false,
        agentSkillPolicyDiagnostics: {
          dangling_disabled_refs: [],
        },
      }));
      return [];
    }
    setState((prev) => ({ ...prev, agentSkillsLoading: true }));
    try {
      const { data: res } = await listAgentSkills({ path: { id }, throwOnError: true });
      const agentSkills = res?.skills ?? [];
      setState((prev) => ({
        ...prev,
        // SAFETY: the agent's skills response items are Skill-shaped.
        agentSkills: agentSkills as Skill[],
        agentSkillsLoading: false,
        agentSkillCanManageActivation: res?.can_manage_activation ?? false,
        agentSkillPolicyDiagnostics: res?.policy_diagnostics ?? {
          dangling_disabled_refs: [],
        },
      }));
      return agentSkills;
    } catch {
      setState((prev) => ({
        ...prev,
        agentSkills: [],
        agentSkillsLoading: false,
        agentSkillCanManageActivation: false,
        agentSkillPolicyDiagnostics: {
          dangling_disabled_refs: [],
        },
      }));
      return [];
    }
  }, []);

  const setAgentSkillActivation = useCallback(
    async (skillRef: string, enabled: boolean) => {
      const current = state;
      const id = current.editingId;
      if (!id || !skillRef || current.agentSkillActivationPending) return;
      setState((prev) => ({ ...prev, agentSkillActivationPending: true }));
      try {
        const { data: activation } = await updateAgentSkillActivation({
          path: { id, skillRef },
          body: { enabled },
          throwOnError: true,
        });
        await loadAgentSkills(id);
        if (current.selectedSkill?.logical_ref === skillRef) {
          const { data: detail } = await getAgentSkill({
            path: { id, skillId: current.selectedSkill.name },
            query: {
              scope: toSkillScope(current.selectedSkill.scope),
            },
            throwOnError: true,
          });
          setState((prev) => ({
            ...prev,
            // SAFETY: getAgentSkill returns the full Skill detail to select.
            selectedSkill: { ...(detail as Skill), scope: current.selectedSkill!.scope },
          }));
        }
        setState((prev) => {
          if (prev.selectedSkill?.logical_ref !== skillRef) return prev;
          return {
            ...prev,
            selectedSkill: { ...prev.selectedSkill, enabled: activation?.enabled ?? enabled },
          };
        });
      } catch (error) {
        showToast(apiErrorMessage(error, t("common.error")), "error");
      } finally {
        setState((prev) => ({ ...prev, agentSkillActivationPending: false }));
      }
    },
    [loadAgentSkills, showToast, state, t],
  );

  const toggleAgentSkillActivation = useCallback(
    (skill: Skill, enabled: boolean) => {
      if (skill.logical_ref) void setAgentSkillActivation(skill.logical_ref, enabled);
    },
    [setAgentSkillActivation],
  );

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
        // SAFETY: each listAgentUsers item maps to the User shape the panel reads.
        (u) => ({ id: u.id ?? "", email: u.username ?? "", name: "" }) as User,
      );
      setState((prev) => ({ ...prev, assignedUsers }));
    } catch {
      setState((prev) => ({ ...prev, assignedUsers: [] }));
    }
  }, []);

  // The user list used to load when its tab was opened; as a section it is
  // always one scroll away, so an admin editing an existing agent fetches it
  // up front. A non-admin only sees the section's visibility control, never the
  // assignment list, so they never make this call.
  useEffect(() => {
    if (!agentId || !data.isAdmin) return;
    void loadAssignedUsers(agentId);
  }, [agentId, data.isAdmin, loadAssignedUsers]);

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
            // SAFETY: agentRequestBody builds the accepted UpdateAgentData body.
            body: agentRequestBody(payload) as UpdateAgentData["body"],
            throwOnError: true,
          });
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
          await Promise.all([loadAgentSkills(newId), loadPersonalisation(newId)]);
          invalidateAgent(newId);
          showToast(t("common.saved"));
          onSaved?.(newId);
        }
      } catch (e) {
        showToast(apiErrorMessage(e, t("common.error")), "error");
      }
    },
    [loadAgentSkills, loadPersonalisation, invalidateAgent, showToast, onSaved, t],
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
          query: { path, scope: toSkillScope(skill.scope) },
          throwOnError: true,
        });
        // SAFETY: getAgentSkillFile returns the file body under data.
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
          query: { scope: toSkillScope(sk.scope) },
          throwOnError: true,
        });
        // SAFETY: getAgentSkill returns the full Skill detail.
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
          query: { scope: toSkillScope(selectedSkill.scope) },
          // SAFETY: the update body is built from the selected skill's edited fields.
          body: {
            expected_digest: selectedSkill.content_digest,
            description: selectedSkill.description,
            disable_model_invocation: !!selectedSkill.disable_model_invocation,
            ...(activeFileEditable
              ? { files: { [selectedSkillActiveFile]: selectedSkillFileContent } }
              : undefined),
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
          query: { scope: toSkillScope(selectedSkill.scope) },
          throwOnError: true,
        });
        setState((prev) => ({
          ...prev,
          // SAFETY: getAgentSkill returns the full Skill detail to select.
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
          query: {
            scope: toSkillScope(sk.scope),
            ...(sk.content_digest ? { expected_digest: sk.content_digest } : undefined),
          },
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
          // SAFETY: { source, scope } matches the install body's accepted shape.
          body: { source, scope } as InstallAgentSkillData["body"],
          throwOnError: true,
        });
        showToast(t("agents.detail.skillInstalled", { name: res?.name ?? "skill" }));
        setState((prev) => ({ ...prev, skillInstallModalOpen: false }));
        const updated = await loadAgentSkills(currentState.editingId);
        const created = updated.find((sk) => sk.name === (res?.name ?? ""));
        if (created) {
          // SAFETY: created is the installed skill with the chosen scope applied.
          await selectSkill({ ...created, scope } as Skill, {
            ...currentState,
            // SAFETY: the reloaded skill list is Skill-shaped.
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
          // SAFETY: created is the installed skill with the chosen scope applied.
          await selectSkill({ ...created, scope } as Skill, {
            ...currentState,
            // SAFETY: the reloaded skill list is Skill-shaped.
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
            scope: toSkillScope(selectedSkill.scope),
            ...(selectedSkill.content_digest
              ? { expected_digest: selectedSkill.content_digest }
              : undefined),
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
        // SAFETY: the skill's metadata object carries string-valued fields.
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
          onToggleActivation={toggleAgentSkillActivation}
          onClearDanglingActivation={(ref) => void setAgentSkillActivation(ref, true)}
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
    </>
  );
}
