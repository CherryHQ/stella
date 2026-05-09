import type { Skill, User } from "@/lib/types";
import type { AgentsPageState } from "./AgentsPage";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { useI18n } from "@/lib/i18n";
import { ConfigTab } from "./tabs/ConfigTab";
import { PromptTab } from "./tabs/PromptTab";
import { SkillsTab } from "./tabs/SkillsTab";
import { AdvancedTab } from "./tabs/AdvancedTab";
import { UsersTab } from "./tabs/UsersTab";
import { PersonalTab } from "./tabs/PersonalTab";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
  onSave: () => void;
  onCancel: () => void;
  onLoadAssignedUsers: (agentId: string) => void;
  onAddUser: () => void;
  onRemoveUser: (userId: number) => void;
  onApplySoul: (soulID: string) => void;
  onSelectSkill: (sk: Skill) => void;
  onToggleSkillStatus: (sk: Skill) => void;
  onSaveSelectedSkill: () => void;
  onDeleteSkill: (sk: Skill) => void;
  onDuplicateBuiltinToAgent: () => void;
  onSelectSkillFile: (path: string, skipDirtyCheck?: boolean) => void;
  onDeleteSkillFile: () => void;
  onSavePersonalisationSoul: () => void;
  onSavePersonalisationProfile: () => void;
  onOpenSkillInstallModal: (scope?: "user" | "agent") => void;
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
  onDuplicateBuiltinToAgent,
  onSelectSkillFile,
  onDeleteSkillFile,
  onSavePersonalisationSoul,
  onSavePersonalisationProfile,
  onOpenSkillInstallModal,
}: Props) {
  const { t } = useI18n();
  const { showForm, editingId, activeTab, isAdmin, form } = state;

  if (!showForm) {
    return (
      <main className="px-6 py-6 min-w-0 hidden lg:block">
        <div className="flex flex-col items-center justify-center min-h-64 text-center gap-4 min-w-0">
          <p className="text-muted-foreground text-sm">
            Select an agent to edit, or create a new one.
          </p>
        </div>
      </main>
    );
  }

  const availableUsers = state.allUsers.filter(
    (u: User) => !state.assignedUsers.some((a: User) => a.id === u.id),
  );

  return (
    <main className="px-6 py-6 min-w-0 block">
      <div className="rounded-xl border border-border overflow-hidden">
        <div className="border-b border-border px-4 py-3 bg-muted/30">
          <span className="font-medium text-sm">
            {editingId ? `Edit: ${form.name}` : "New agent"}
          </span>
        </div>
        <Tabs
          value={activeTab}
          onValueChange={(tab) => {
            onSetState({ activeTab: tab as string });
            if (tab === "users" && editingId) onLoadAssignedUsers(editingId);
          }}
        >
          <TabsList
            variant="underline"
            className="w-full justify-start px-2 border-b border-border rounded-none bg-muted/20 gap-0"
          >
            <TabsTrigger value="config">{t("agents.tabs.config")}</TabsTrigger>
            <TabsTrigger value="prompt">{t("agents.tabs.prompt")}</TabsTrigger>
            <TabsTrigger value="skills">{t("agents.tabs.skills")}</TabsTrigger>
            <TabsTrigger value="advanced">{t("agents.tabs.advanced")}</TabsTrigger>
            {isAdmin && <TabsTrigger value="users">{t("agents.tabs.users")}</TabsTrigger>}
            {editingId && <TabsTrigger value="personal">{t("agents.tabs.personal")}</TabsTrigger>}
          </TabsList>
          <div className="p-4 space-y-4">
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
                onDuplicateBuiltinToAgent={onDuplicateBuiltinToAgent}
                onSelectSkillFile={onSelectSkillFile}
                onDeleteSkillFile={onDeleteSkillFile}
                onOpenSkillInstallModal={onOpenSkillInstallModal}
              />
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
            <TabsContent value="personal">
              <PersonalTab
                state={state}
                onSetState={onSetState}
                onSaveSoul={onSavePersonalisationSoul}
                onSaveProfile={onSavePersonalisationProfile}
              />
            </TabsContent>
          </div>
        </Tabs>
        <div className="border-t border-border px-4 py-3 flex items-center justify-end gap-2 bg-muted/20">
          <Button onClick={onCancel} variant="ghost" size="sm">
            {t("common.cancel")}
          </Button>
          <Button onClick={onSave} size="sm">
            {editingId ? t("common.update") : t("common.create")}
          </Button>
        </div>
      </div>
    </main>
  );
}
