import { useCallback, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  deleteAuthUserIdentity,
  getAuthUser,
  getMe,
  listAgents,
  listUserMemories,
  setUserMemory,
  deleteUserMemory,
  updateAuthUserActive,
  updateAuthUserAgents,
  updateAuthUserRole,
  updateUserDefaultAgent,
  updateUserNotifyIdentity,
} from "@/lib/api-client/sdk.gen";
import { authUsersQueryOptions, authUserAgentsOptions } from "@/lib/queries/users";
import type { Agent, ChannelIdentity, User, UserMemory } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Textarea } from "@/components/ui/textarea";
import { useI18n } from "@/lib/i18n";
import { errorMessage } from "@/lib/utils";
import { useToast } from "@/hooks/use-toast";
import {
  DetailPanel,
  DetailPanelHeader,
  FormSectionTitle,
} from "@/features/settings/SettingsDetailPanel";
import { ConfirmDialog } from "@/features/settings/ConfirmDialog";

interface UserDetailPanelProps {
  userId: string;
}

export function UserDetailPanel({ userId }: UserDetailPanelProps) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const queryClient = useQueryClient();
  const [user, setUser] = useState<User | null>(null);
  const [currentUserId, setCurrentUserId] = useState("");
  const [agents, setAgents] = useState<Agent[]>([]);
  const [addAgentId, setAddAgentId] = useState("");
  const [confirmState, setConfirmState] = useState<{
    message: string;
    onConfirm: () => void;
  } | null>(null);

  // Memory state
  const [memories, setMemories] = useState<(UserMemory & { _content: string })[]>([]);
  const [defaultAgent, setDefaultAgent] = useState("");
  const [showAddMemory, setShowAddMemory] = useState(false);
  const [newMemoryAgent, setNewMemoryAgent] = useState("");
  const [newMemoryContent, setNewMemoryContent] = useState("");

  const { data: userAgentIds = [] } = useQuery(authUserAgentsOptions(userId));

  const loadUser = useCallback(async () => {
    try {
      const { data } = await getAuthUser({ path: { id: userId }, throwOnError: true });
      // SAFETY: getAuthUser returns the user record under data.
      const loadedUser = data as User;
      setUser(loadedUser);
      setDefaultAgent(loadedUser?.default_agent_id ?? "");
    } catch (e) {
      showToast(errorMessage(e), "error");
    }
  }, [userId, showToast]);

  const loadMemories = useCallback(async () => {
    try {
      const { data } = await listUserMemories({ path: { id: userId }, throwOnError: true });
      // SAFETY: listUserMemories returns UserMemory items under data.memories.
      const mems = (data?.memories as UserMemory[]) ?? [];
      setMemories(mems.map((m) => ({ ...m, _content: m.content })));
    } catch (e) {
      showToast(errorMessage(e), "error");
    }
  }, [userId, showToast]);

  useEffect(() => {
    void loadUser();
    void getMe({ throwOnError: true })
      .then(({ data }) => {
        if (data?.id) setCurrentUserId(data.id);
      })
      .catch(() => {});
    void listAgents({ query: { include_all: true }, throwOnError: true })
      .then(({ data }) => {
        // SAFETY: listAgents returns Agent items under data.agents.
        const agents = (data?.agents ?? []) as Agent[];
        setAgents(agents);
      })
      .catch(() => {});
  }, [loadUser]);

  useEffect(() => {
    void loadMemories();
  }, [loadMemories]);

  const invalidateUsers = () => {
    void queryClient.invalidateQueries({ queryKey: authUsersQueryOptions.queryKey });
    void queryClient.invalidateQueries({ queryKey: ["auth-user-agents", userId] });
  };

  const roleMutation = useMutation({
    mutationFn: async (role: string) => {
      await updateAuthUserRole({ path: { id: userId }, body: { role }, throwOnError: true });
    },
    onSuccess: async (_data, role) => {
      await loadUser();
      invalidateUsers();
      showToast(t("users.roleUpdated", { role }));
    },
    onError: (e) => showToast(errorMessage(e), "error"),
  });

  const activeMutation = useMutation({
    mutationFn: async (isActive: boolean) => {
      await updateAuthUserActive({
        path: { id: userId },
        body: { is_active: isActive },
        throwOnError: true,
      });
    },
    onSuccess: async (_data, isActive) => {
      await loadUser();
      invalidateUsers();
      showToast(isActive ? t("users.userActivated") : t("users.userDeactivated"));
    },
    onError: (e) => showToast(errorMessage(e), "error"),
  });

  const agentsMutation = useMutation({
    mutationFn: async (agentIds: string[]) => {
      await updateAuthUserAgents({
        path: { id: userId },
        body: { agent_ids: agentIds },
        throwOnError: true,
      });
    },
    onSuccess: () => {
      invalidateUsers();
      setAddAgentId("");
    },
    onError: (e) => showToast(errorMessage(e), "error"),
  });

  const unlinkMutation = useMutation({
    mutationFn: async (identityId: string) => {
      await deleteAuthUserIdentity({ path: { id: userId, identityId }, throwOnError: true });
    },
    onSuccess: async () => {
      await loadUser();
      invalidateUsers();
      showToast(t("users.identityUnlinked"));
    },
    onError: (e) => showToast(errorMessage(e), "error"),
  });

  const notifyMutation = useMutation({
    mutationFn: async (identityId: string) => {
      await updateUserNotifyIdentity({
        path: { id: userId },
        body: { notify_identity_id: identityId || null },
        throwOnError: true,
      });
    },
    onSuccess: async () => {
      await loadUser();
      showToast(t("users.notifyUpdated"));
    },
    onError: (e) => showToast(errorMessage(e), "error"),
  });

  const defaultAgentMutation = useMutation({
    mutationFn: async (agentId: string) => {
      await updateUserDefaultAgent({
        path: { id: userId },
        body: { default_agent_id: agentId },
        throwOnError: true,
      });
    },
    onSuccess: () => showToast(t("common.save")),
    onError: (e) => showToast(errorMessage(e), "error"),
  });

  const saveMemoryMutation = useMutation({
    mutationFn: async ({ agentId, content }: { agentId: string; content: string }) => {
      await setUserMemory({ path: { id: userId, agentId }, body: { content }, throwOnError: true });
    },
    onSuccess: () => {
      void loadMemories();
      showToast(t("common.save"));
    },
    onError: (e) => showToast(errorMessage(e), "error"),
  });

  const deleteMemoryMutation = useMutation({
    mutationFn: async (agentId: string) => {
      await deleteUserMemory({ path: { id: userId, agentId }, throwOnError: true });
    },
    onSuccess: () => {
      void loadMemories();
      showToast(t("users.deleted"));
    },
    onError: (e) => showToast(errorMessage(e), "error"),
  });

  const addMemoryMutation = useMutation({
    mutationFn: async ({ agentId, content }: { agentId: string; content: string }) => {
      await setUserMemory({ path: { id: userId, agentId }, body: { content }, throwOnError: true });
    },
    onSuccess: () => {
      setShowAddMemory(false);
      setNewMemoryAgent("");
      setNewMemoryContent("");
      void loadMemories();
      showToast(t("common.save"));
    },
    onError: (e) => showToast(errorMessage(e), "error"),
  });

  if (!user) return <div className="p-6 text-xs text-muted-foreground">{t("common.loading")}</div>;

  const availableAgents = agents.filter((a) => !userAgentIds.includes(a.id));

  return (
    <DetailPanel>
      <DetailPanelHeader
        title={user.name || user.email}
        subtitle={
          <div className="flex items-center gap-2 mt-1">
            <Badge variant={user.role === "admin" ? "default" : "outline"} size="sm">
              {user.role}
            </Badge>
            {!user.is_active && (
              <Badge variant="error" size="sm">
                {t("users.inactive")}
              </Badge>
            )}
          </div>
        }
      />

      <div className="space-y-6">
        <div className="space-y-4">
          <div className="flex items-center gap-3">
            <Badge variant={user.is_active ? "success" : "error"}>
              {user.is_active ? t("users.active") : t("users.inactive")}
            </Badge>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => activeMutation.mutate(!user.is_active)}
              className={user.is_active ? "text-destructive-foreground" : "text-success-foreground"}
            >
              {user.is_active ? t("users.deactivate") : t("users.activate")}
            </Button>
          </div>

          <div>
            <div className="mb-2">
              <FormSectionTitle>{t("users.role")}</FormSectionTitle>
            </div>
            <div className="flex items-center gap-2">
              <Badge variant={user.role === "admin" ? "default" : "outline"}>{user.role}</Badge>
              {user.role !== "admin" && (
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => roleMutation.mutate("admin")}
                  className="text-primary"
                >
                  {t("users.promoteAdmin")}
                </Button>
              )}
              {user.role === "admin" && (
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => roleMutation.mutate("user")}
                  disabled={user.id === currentUserId}
                  title={user.id === currentUserId ? t("users.cannotDemoteSelf") : undefined}
                  className="text-destructive-foreground"
                >
                  {t("users.demoteUser")}
                </Button>
              )}
            </div>
          </div>

          <div>
            <div className="mb-2">
              <FormSectionTitle>{t("users.linkedIdentities")}</FormSectionTitle>
            </div>
            <div className="space-y-2">
              {(user.identities || []).map((ident: ChannelIdentity) => (
                <div
                  key={ident.id}
                  className="flex items-center justify-between py-1 px-2 bg-muted rounded"
                >
                  <div className="flex items-center gap-2">
                    <Badge variant="outline" size="sm" className="font-mono">
                      {ident.platform}
                    </Badge>
                    <span className="text-sm font-mono">{ident.external_id}</span>
                    {ident.name && (
                      <span className="text-xs text-muted-foreground">{ident.name}</span>
                    )}
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-muted-foreground">{ident.created_at}</span>
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() =>
                        setConfirmState({
                          message: t("users.unlinkIdentity", { platform: ident.platform }),
                          onConfirm: () => unlinkMutation.mutate(ident.id),
                        })
                      }
                      className="text-destructive-foreground"
                    >
                      {t("common.remove")}
                    </Button>
                  </div>
                </div>
              ))}
              {(!user.identities || user.identities.length === 0) && (
                <div className="text-xs text-muted-foreground py-2">{t("users.noIdentities")}</div>
              )}
            </div>
          </div>

          {user.identities && user.identities.length > 0 && (
            <div>
              <div className="mb-2">
                <FormSectionTitle>{t("users.notifyChannel")}</FormSectionTitle>
              </div>
              <select
                value={user.notify_identity_id ?? ""}
                onChange={(e) => notifyMutation.mutate(e.target.value)}
                className="h-9 w-full rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8"
              >
                <option value="">{t("users.autoFirst")}</option>
                {user.identities.map((ident: ChannelIdentity) => (
                  <option key={ident.id} value={ident.id}>
                    {ident.platform}
                    {ident.name ? ` — ${ident.name}` : ` — ${ident.external_id}`}
                  </option>
                ))}
              </select>
              <p className="text-xs text-muted-foreground mt-1">{t("users.notifyChannelDesc")}</p>
            </div>
          )}

          <div>
            <div className="mb-2">
              <FormSectionTitle>{t("users.agentAssignments")}</FormSectionTitle>
            </div>
            <div className="space-y-2">
              {userAgentIds.map((aid) => (
                <div
                  key={aid}
                  className="flex items-center justify-between py-1 px-2 bg-muted rounded"
                >
                  <span className="text-sm font-mono">{aid}</span>
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => {
                      const newIds = userAgentIds.filter((id) => id !== aid);
                      agentsMutation.mutate(newIds);
                      showToast(t("users.agentRemoved"));
                    }}
                    className="text-destructive-foreground"
                  >
                    {t("common.remove")}
                  </Button>
                </div>
              ))}
              {userAgentIds.length === 0 && (
                <div className="text-xs text-muted-foreground py-2">
                  {t("users.noAgentAssignments")}
                </div>
              )}
            </div>
            <div className="mt-3 flex gap-2">
              <select
                value={addAgentId}
                onChange={(e) => setAddAgentId(e.target.value)}
                className="h-9 flex-1 rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8"
              >
                <option value="">{t("users.selectAgent")}</option>
                {availableAgents.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name} ({a.id})
                  </option>
                ))}
              </select>
              <Button
                size="sm"
                onClick={() => {
                  agentsMutation.mutate([...userAgentIds, addAgentId]);
                  showToast(t("users.agentAssigned"));
                }}
                disabled={!addAgentId}
              >
                {t("common.add")}
              </Button>
            </div>
          </div>

          <div className="text-xs text-muted-foreground space-y-1 pt-2 border-t border-border">
            <p>
              {t("users.createdAt")} {user.created_at}
            </p>
            <p>
              {t("users.updatedAt")} {user.updated_at}
            </p>
          </div>
        </div>

        <div className="space-y-4 border-t border-border pt-6">
          <FormSectionTitle>{t("users.memoryTab")}</FormSectionTitle>
          <div className="flex items-center gap-3">
            <span className="text-xs font-medium text-muted-foreground shrink-0">
              {t("users.defaultAgent")}
            </span>
            <select
              value={defaultAgent}
              onChange={(e) => setDefaultAgent(e.target.value)}
              className="h-9 flex-1 rounded-lg border border-input bg-background px-3 text-sm outline-none sm:h-8"
            >
              <option value="">{t("users.none")}</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name} ({a.id})
                </option>
              ))}
            </select>
            <Button
              size="sm"
              onClick={() => defaultAgentMutation.mutate(defaultAgent)}
              disabled={defaultAgent === (user.default_agent_id || "")}
            >
              {t("common.save")}
            </Button>
          </div>

          <div>
            <p className="text-xs font-semibold text-muted-foreground mb-3">
              {t("users.memory")}
              {memories.length > 0 && (
                <span className="text-primary ml-1">({memories.length})</span>
              )}
            </p>
            <div className="space-y-4">
              {memories.map((mem) => (
                <div key={mem.agent_id}>
                  <div className="flex items-center justify-between mb-1">
                    <span className="text-xs font-mono font-medium text-primary">
                      {mem.agent_id}
                    </span>
                    <span className="text-xs text-muted-foreground">{mem.updated_at}</span>
                  </div>
                  <Textarea
                    value={mem._content}
                    onChange={(e) =>
                      setMemories((prev) =>
                        prev.map((m) =>
                          m.agent_id === mem.agent_id ? { ...m, _content: e.target.value } : m,
                        ),
                      )
                    }
                    rows={2}
                    className="w-full text-xs font-mono"
                  />
                  <div className="flex items-center gap-2 mt-1">
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() =>
                        setConfirmState({
                          message: `Delete memory for ${mem.agent_id}?`,
                          onConfirm: () => deleteMemoryMutation.mutate(mem.agent_id),
                        })
                      }
                      className="text-muted-foreground hover:text-destructive-foreground"
                    >
                      {t("common.delete")}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() =>
                        saveMemoryMutation.mutate({
                          agentId: mem.agent_id,
                          content: mem._content,
                        })
                      }
                      disabled={mem._content === mem.content}
                      className="text-primary"
                    >
                      {t("common.save")}
                    </Button>
                  </div>
                </div>
              ))}
              {memories.length === 0 && !showAddMemory && (
                <div className="text-xs text-muted-foreground py-2">{t("users.noMemories")}</div>
              )}
              {showAddMemory && (
                <div className="space-y-2">
                  <select
                    value={newMemoryAgent}
                    onChange={(e) => setNewMemoryAgent(e.target.value)}
                    className="h-9 w-full rounded-lg border border-input bg-background px-3 text-xs outline-none sm:h-8"
                  >
                    <option value="" disabled>
                      {t("users.selectAgent")}
                    </option>
                    {agents
                      .filter((a) => !memories.some((m) => m.agent_id === a.id))
                      .map((a) => (
                        <option key={a.id} value={a.id}>
                          {a.name} ({a.id})
                        </option>
                      ))}
                  </select>
                  <Textarea
                    value={newMemoryContent}
                    onChange={(e) => setNewMemoryContent(e.target.value)}
                    rows={2}
                    placeholder={t("users.memoryContent")}
                    className="w-full text-xs font-mono"
                  />
                  <div className="flex gap-2">
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => setShowAddMemory(false)}
                      className="text-muted-foreground"
                    >
                      {t("common.cancel")}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() =>
                        addMemoryMutation.mutate({
                          agentId: newMemoryAgent,
                          content: newMemoryContent,
                        })
                      }
                      disabled={!newMemoryAgent || !newMemoryContent}
                      className="text-primary"
                    >
                      {t("common.add")}
                    </Button>
                  </div>
                </div>
              )}
              {!showAddMemory && (
                <Button
                  type="button"
                  variant="link"
                  size="xs"
                  onClick={() => {
                    setShowAddMemory(true);
                    setNewMemoryAgent("");
                    setNewMemoryContent("");
                  }}
                >
                  + {t("users.addMemory")}
                </Button>
              )}
            </div>
          </div>
        </div>
      </div>

      <ConfirmDialog
        open={!!confirmState}
        onOpenChange={(open) => {
          if (!open) setConfirmState(null);
        }}
        title={t("common.confirm")}
        message={confirmState?.message ?? ""}
        onConfirm={() => confirmState?.onConfirm()}
        confirmLabel={t("common.confirm")}
      />
    </DetailPanel>
  );
}
