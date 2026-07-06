import { useNavigate } from "@tanstack/react-router";
import type { Skill, User } from "@/lib/types";
import type { AgentsPageState } from "./AgentsPage";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { useI18n } from "@/lib/i18n";
import { ConfigTab } from "./tabs/ConfigTab";
import { PromptTab } from "./tabs/PromptTab";
import { SkillsTab } from "./tabs/SkillsTab";
import { ToolsTab } from "./tabs/ToolsTab";
import { AdvancedTab } from "./tabs/AdvancedTab";
import { UsersTab } from "./tabs/UsersTab";
interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onSave: () => void;
  onCancel: () => void;
  onLoadAssignedUsers: (agentId: string) => void;
  onAddUser: () => void;
  onRemoveUser: (userId: string) => void;
  onApplySoul: (soulID: string) => void;
  onSelectSkill: (sk: Skill) => void;
  onToggleSkillStatus: (sk: Skill) => void;
  onSaveSelectedSkill: () => void;
  onDeleteSkill: (sk: Skill) => void;
  onSelectSkillFile: (path: string, skipDirtyCheck?: boolean) => void;
  onDeleteSkillFile: () => void;
  onOpenSkillInstallModal: (scope?: "user_agent" | "system_agent") => void;
  onDelete?: () => void;
}

export function AgentForm({
  state,
  onSetState,
  onSave,
  onCancel,
  onLoadAssignedUsers,
  onAddUser,
  onRemoveUser,
  onApplySoul,
  onSelectSkill,
  onToggleSkillStatus,
  onSaveSelectedSkill,
  onDeleteSkill,
  onSelectSkillFile,
  onDeleteSkillFile,
  onOpenSkillInstallModal,
  onDelete,
}: Props) {
  const navigate = useNavigate();
  const { t } = useI18n();
  const { editingId, activeTab, isAdmin, form, currentUserId } = state;

  const canEdit = isAdmin || !editingId || (form.creator_id && form.creator_id === currentUserId);

  const availableUsers = state.allUsers.filter(
    (u: User) => !state.assignedUsers.some((a: User) => a.id === u.id),
  );

  return (
    <div className="h-full min-h-0 min-w-0 flex flex-col bg-card">
      <div className="border-b border-border px-6 py-4">
        <span className="font-medium text-sm text-foreground">
          {editingId ? t("agents.form.editAgent", { name: form.name }) : t("agents.form.newAgent")}
        </span>
      </div>
      <Tabs
        value={activeTab}
        onValueChange={(tab) => {
          onSetState({ activeTab: tab as string });
          if (editingId) {
            void navigate({
              to: "/settings/agents/$agentId/$tab",
              params: { agentId: editingId, tab: tab as string },
            });
          }
          if (tab === "users" && editingId) onLoadAssignedUsers(editingId);
        }}
        className="flex-1 flex flex-col min-h-0"
      >
        <TabsList
          variant="underline"
          className="w-full justify-start px-6 border-b border-border rounded-none bg-transparent gap-2 h-11"
        >
          <TabsTrigger value="config">{t("agents.tabs.config")}</TabsTrigger>
          <TabsTrigger value="prompt">{t("agents.tabs.prompt")}</TabsTrigger>
          <TabsTrigger value="skills">{t("agents.tabs.skills")}</TabsTrigger>
          <TabsTrigger value="tools">{t("agents.tabs.tools")}</TabsTrigger>
          <TabsTrigger value="advanced">{t("agents.tabs.advanced")}</TabsTrigger>
          {isAdmin && <TabsTrigger value="users">{t("agents.tabs.users")}</TabsTrigger>}
        </TabsList>
        <div className="flex-1 min-h-0 overflow-y-auto p-6 space-y-6">
          <TabsContent value="config">
            <ConfigTab state={state} onSetState={onSetState} />
          </TabsContent>
          <TabsContent value="prompt">
            <PromptTab state={state} onSetState={onSetState} onApplySoul={onApplySoul} />
          </TabsContent>
          <TabsContent value="skills">
            <SkillsTab
              state={state}
              onSetState={onSetState}
              onSelectSkill={onSelectSkill}
              onToggleSkillStatus={onToggleSkillStatus}
              onSaveSelectedSkill={onSaveSelectedSkill}
              onDeleteSkill={onDeleteSkill}
              onSelectSkillFile={onSelectSkillFile}
              onDeleteSkillFile={onDeleteSkillFile}
              onOpenSkillInstallModal={onOpenSkillInstallModal}
            />
          </TabsContent>
          <TabsContent value="tools">
            <ToolsTab state={state} />
          </TabsContent>
          <TabsContent value="advanced">
            <AdvancedTab state={state} onSetState={onSetState} />
          </TabsContent>
          <TabsContent value="users">
            <UsersTab
              state={state}
              availableUsers={availableUsers}
              onSetState={onSetState}
              onAddUser={onAddUser}
              onRemoveUser={onRemoveUser}
            />
          </TabsContent>
        </div>
      </Tabs>
      <div className="shrink-0 border-t border-border px-6 py-4 flex items-center justify-between gap-2">
        <div>
          {canEdit && editingId && onDelete && (
            <Button
              onClick={onDelete}
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-destructive cursor-pointer duration-120"
            >
              {t("common.delete")}
            </Button>
          )}
        </div>
        <div className="flex items-center gap-2">
          <Button onClick={onCancel} variant="ghost" size="sm" className="cursor-pointer">
            {t("common.cancel")}
          </Button>
          {canEdit && (
            <Button onClick={onSave} size="sm" className="cursor-pointer">
              {editingId ? t("common.update") : t("common.create")}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
