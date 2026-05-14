import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { Agent, Identity, User, UserMemory } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogPopup, DialogPanel, DialogFooter } from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";
import { SettingsDetailLayout } from "@/features/settings/SettingsDetailLayout";

interface Toast {
  message: string;
  type: "success" | "error";
}

interface ConfirmDialog {
  message: string;
  onConfirm: () => void;
}

interface LegacyUser extends User {
  _defaultAgent: string;
  _showMemory: boolean;
  _memoryCount: number;
  _showAddMemory: boolean;
  _newMemoryAgent: string;
  _newMemoryContent: string;
}

export function UsersPage() {
  const { t } = useI18n();
  const [tab, setTab] = useState<"auth" | "memory">("auth");
  const [currentUserId, setCurrentUserId] = useState(0);
  const [authUsers, setAuthUsers] = useState<User[]>([]);
  const [selectedUser, setSelectedUser] = useState<User | null>(null);
  const [userAgentIds, setUserAgentIds] = useState<string[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [addAgentId, setAddAgentId] = useState("");
  const [legacyUsers, setLegacyUsers] = useState<LegacyUser[]>([]);
  const [userMemories, setUserMemories] = useState<
    Record<number, (UserMemory & { _content: string })[]>
  >({});
  const [toast, setToast] = useState<Toast | null>(null);
  const [confirm, setConfirm] = useState<ConfirmDialog | null>(null);
  const toastTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const showToast = useCallback((message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    if (toastTimer.current) clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => setToast(null), 3000);
  }, []);

  const confirmDelete = useCallback((message: string, onConfirm: () => void) => {
    setConfirm({ message, onConfirm });
  }, []);

  const loadAuthUsers = useCallback(async () => {
    try {
      const users = (await api<User[]>("GET", "/api/auth/users")) ?? [];
      setAuthUsers(users);
    } catch (e) {
      console.error(e);
    }
  }, []);

  const loadUserAgents = useCallback(async (userId: number) => {
    try {
      const ids = (await api<string[]>("GET", `/api/auth/users/${userId}/agents`)) ?? [];
      setUserAgentIds(ids);
      setAddAgentId("");
    } catch {
      setUserAgentIds([]);
    }
  }, []);

  const selectUser = useCallback(
    async (u: User) => {
      try {
        const detail = await api<User>("GET", `/api/auth/users/${u.id}`);
        setSelectedUser(detail);
        await loadUserAgents(u.id);
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [loadUserAgents, showToast],
  );

  const setRole = useCallback(
    async (role: string) => {
      if (!selectedUser) return;
      try {
        await api("PUT", `/api/auth/users/${selectedUser.id}/role`, { role });
        const updated = await api<User>("GET", `/api/auth/users/${selectedUser.id}`);
        setSelectedUser(updated);
        await loadAuthUsers();
        showToast(`Role updated to ${role}`);
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [selectedUser, loadAuthUsers, showToast],
  );

  const toggleActive = useCallback(async () => {
    if (!selectedUser) return;
    const newActive = !selectedUser.is_active;
    try {
      await api("PUT", `/api/auth/users/${selectedUser.id}/active`, { is_active: newActive });
      const updated = await api<User>("GET", `/api/auth/users/${selectedUser.id}`);
      setSelectedUser(updated);
      await loadAuthUsers();
      showToast(newActive ? "User activated" : "User deactivated");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [selectedUser, loadAuthUsers, showToast]);

  const addAgentToUser = useCallback(async () => {
    if (!selectedUser || !addAgentId) return;
    const newIds = [...userAgentIds, addAgentId];
    try {
      await api("PUT", `/api/auth/users/${selectedUser.id}/agents`, { agent_ids: newIds });
      await loadUserAgents(selectedUser.id);
      showToast("Agent assigned");
    } catch (e) {
      showToast((e as Error).message, "error");
    }
  }, [selectedUser, addAgentId, userAgentIds, loadUserAgents, showToast]);

  const removeAgentFromUser = useCallback(
    async (agentId: string) => {
      if (!selectedUser) return;
      const newIds = userAgentIds.filter((id) => id !== agentId);
      try {
        await api("PUT", `/api/auth/users/${selectedUser.id}/agents`, { agent_ids: newIds });
        await loadUserAgents(selectedUser.id);
        showToast("Agent removed");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [selectedUser, userAgentIds, loadUserAgents, showToast],
  );

  const unlinkIdentity = useCallback(
    async (identityId: number) => {
      if (!selectedUser) return;
      try {
        await api("DELETE", `/api/auth/users/${selectedUser.id}/identities/${identityId}`);
        const updated = await api<User>("GET", `/api/auth/users/${selectedUser.id}`);
        setSelectedUser(updated);
        await loadAuthUsers();
        showToast("Identity unlinked");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [selectedUser, loadAuthUsers, showToast],
  );

  const setNotifyIdentity = useCallback(
    async (identityId: string) => {
      if (!selectedUser) return;
      try {
        const val = identityId ? parseInt(identityId, 10) : null;
        await api("PUT", `/api/users/${selectedUser.id}/notify-identity`, {
          notify_identity_id: val,
        });
        const updated = await api<User>("GET", `/api/auth/users/${selectedUser.id}`);
        setSelectedUser(updated);
        showToast("Notify channel updated");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [selectedUser, showToast],
  );

  const loadLegacyUsers = useCallback(async () => {
    try {
      const list = (await api<User[]>("GET", "/api/auth/users")) ?? [];
      setLegacyUsers((prev) => {
        const prevMap = new Map(prev.map((u) => [u.id, u]));
        return list.map((u) => {
          const existing = prevMap.get(u.id);
          return {
            ...u,
            _defaultAgent: existing?._defaultAgent ?? u.default_agent_id ?? "",
            _showMemory: existing?._showMemory ?? false,
            _memoryCount: existing?._memoryCount ?? 0,
            _showAddMemory: existing?._showAddMemory ?? false,
            _newMemoryAgent: existing?._newMemoryAgent ?? "",
            _newMemoryContent: existing?._newMemoryContent ?? "",
          };
        });
      });
    } catch (e) {
      console.error(e);
    }
  }, []);

  const loadUserMemories = useCallback(
    async (userId: number) => {
      try {
        const mems = (await api<UserMemory[]>("GET", `/api/users/${userId}/memories`)) ?? [];
        setUserMemories((prev) => ({
          ...prev,
          [userId]: mems.map((m) => ({ ...m, _content: m.content })),
        }));
        setLegacyUsers((prev) =>
          prev.map((u) => (u.id === userId ? { ...u, _memoryCount: mems.length } : u)),
        );
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [showToast],
  );

  const saveUserDefaultAgent = useCallback(
    async (u: LegacyUser) => {
      try {
        await api("PUT", `/api/users/${u.id}`, { default_agent_id: u._defaultAgent });
        setLegacyUsers((prev) =>
          prev.map((lu) => (lu.id === u.id ? { ...lu, default_agent_id: lu._defaultAgent } : lu)),
        );
        showToast("Saved");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [showToast],
  );

  const saveUserMemory = useCallback(
    async (userId: number, agentId: string, content: string) => {
      try {
        await api("PUT", `/api/users/${userId}/memories/${agentId}`, { content });
        await loadUserMemories(userId);
        showToast("Saved");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [loadUserMemories, showToast],
  );

  const doDeleteUserMemory = useCallback(
    async (userId: number, agentId: string) => {
      try {
        await api("DELETE", `/api/users/${userId}/memories/${agentId}`);
        await loadUserMemories(userId);
        showToast("Deleted");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [loadUserMemories, showToast],
  );

  const addUserMemory = useCallback(
    async (u: LegacyUser) => {
      if (!u._newMemoryAgent || !u._newMemoryContent) return;
      try {
        await api("PUT", `/api/users/${u.id}/memories/${u._newMemoryAgent}`, {
          content: u._newMemoryContent,
        });
        setLegacyUsers((prev) =>
          prev.map((lu) =>
            lu.id === u.id
              ? { ...lu, _showAddMemory: false, _newMemoryAgent: "", _newMemoryContent: "" }
              : lu,
          ),
        );
        await loadUserMemories(u.id);
        showToast("Added");
      } catch (e) {
        showToast((e as Error).message, "error");
      }
    },
    [loadUserMemories, showToast],
  );

  useEffect(() => {
    void Promise.all([
      api<{ id: number }>("GET", "/api/auth/me")
        .then((r) => {
          if (r?.id) setCurrentUserId(r.id);
        })
        .catch(() => {}),
      loadAuthUsers(),
      api<Agent[]>("GET", "/api/agents")
        .then((r) => setAgents(r ?? []))
        .catch(() => {}),
    ]);
  }, [loadAuthUsers]);

  useEffect(() => {
    if (tab === "memory") void loadLegacyUsers();
  }, [tab, loadLegacyUsers]);

  useEffect(() => {
    if (tab === "memory" && selectedUser) {
      void loadUserMemories(selectedUser.id);
    }
  }, [tab, selectedUser, loadUserMemories]);

  const availableAgents = agents.filter((a) => !userAgentIds.includes(a.id));

  // The selected legacy user for the memory tab
  const selectedLegacyUser = selectedUser
    ? (legacyUsers.find((u) => u.id === selectedUser.id) ?? null)
    : null;

  // ── Left panel ──────────────────────────────────────────────────────────────

  const listHeader = (
    <div className="px-4 py-3 border-b border-border">
      <h2 className="text-sm font-medium">Users</h2>
    </div>
  );

  const list = (
    <div>
      {authUsers.map((u) => (
        <button
          key={u.id}
          onClick={() => void selectUser(u)}
          className={`w-full text-left px-4 py-3 border-b border-border/50 hover:bg-muted/50 transition-colors ${
            selectedUser?.id === u.id ? "bg-primary/8" : ""
          }`}
        >
          <div className="font-medium text-sm truncate" style={{ fontWeight: 500 }}>
            {u.username}
          </div>
          <div className="text-xs font-mono text-muted-foreground mt-0.5">
            {u.role === "admin" ? (
              <span className="text-primary">{u.role}</span>
            ) : (
              <span>{u.role}</span>
            )}
          </div>
        </button>
      ))}
    </div>
  );

  // ── Right panel — user detail ────────────────────────────────────────────────

  const detail = selectedUser ? (
    <div className="flex flex-col h-full">
      {/* Detail header */}
      <div className="shrink-0 px-6 py-4 border-b border-border">
        <div className="flex items-center gap-3 mb-3">
          <h2 className="text-lg font-medium">{selectedUser.username}</h2>
          <Badge variant={selectedUser.role === "admin" ? "default" : "outline"} size="sm">
            {selectedUser.role}
          </Badge>
          {!selectedUser.is_active && (
            <Badge variant="error" size="sm">
              inactive
            </Badge>
          )}
        </div>
        {/* Tab switcher */}
        <div className="flex gap-4 border-b border-border -mb-4 pb-0">
          <button
            onClick={() => setTab("auth")}
            className={`pb-2 text-sm font-medium transition-colors border-b-2 ${
              tab === "auth"
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            Auth
          </button>
          <button
            onClick={() => setTab("memory")}
            className={`pb-2 text-sm font-medium transition-colors border-b-2 ${
              tab === "memory"
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            Memory
          </button>
        </div>
      </div>

      {/* Tab content */}
      <div className="flex-1 overflow-y-auto px-6 py-4">
        {/* Auth Tab */}
        {tab === "auth" && (
          <div className="space-y-4">
            {/* Status + Actions */}
            <div className="flex items-center gap-3">
              <Badge variant={selectedUser.is_active ? "success" : "error"}>
                {selectedUser.is_active ? "Active" : "Inactive"}
              </Badge>
              <Button
                variant="ghost"
                size="xs"
                onClick={() => void toggleActive()}
                className={selectedUser.is_active ? "text-destructive" : "text-success-foreground"}
              >
                {selectedUser.is_active ? "Deactivate" : "Activate"}
              </Button>
            </div>

            {/* Role Section */}
            <div>
              <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-2">
                Role
              </p>
              <div className="flex items-center gap-2">
                <Badge variant={selectedUser.role === "admin" ? "default" : "outline"}>
                  {selectedUser.role}
                </Badge>
                {selectedUser.role !== "admin" && (
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => void setRole("admin")}
                    className="text-primary"
                  >
                    promote to admin
                  </Button>
                )}
                {selectedUser.role === "admin" && (
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => void setRole("user")}
                    disabled={selectedUser.id === currentUserId}
                    title={selectedUser.id === currentUserId ? "Cannot demote yourself" : undefined}
                    className="text-destructive"
                  >
                    demote to user
                  </Button>
                )}
              </div>
            </div>

            {/* Identities Section */}
            <div>
              <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-2">
                Linked Identities
              </p>
              <div className="space-y-2">
                {(selectedUser.identities || []).map((ident: Identity) => (
                  <div
                    key={ident.id}
                    className="flex items-center justify-between py-1 px-2 bg-muted rounded"
                  >
                    <div className="flex items-center gap-2">
                      <Badge variant="outline" size="sm" className="font-mono uppercase">
                        {ident.platform}
                      </Badge>
                      <span className="text-sm font-mono">{ident.external_id}</span>
                      {ident.name && (
                        <span className="text-xs text-muted-foreground">{ident.name}</span>
                      )}
                    </div>
                    <div className="flex items-center gap-2">
                      <span className="text-xs text-muted-foreground">{ident.linked_at}</span>
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() =>
                          confirmDelete(
                            `Unlink ${ident.platform} identity?`,
                            () => void unlinkIdentity(ident.id),
                          )
                        }
                        className="text-destructive"
                      >
                        unlink
                      </Button>
                    </div>
                  </div>
                ))}
                {(!selectedUser.identities || selectedUser.identities.length === 0) && (
                  <div className="text-xs text-muted-foreground py-2">No linked identities.</div>
                )}
              </div>
            </div>

            {/* Notify Channel Section */}
            {selectedUser.identities && selectedUser.identities.length > 0 && (
              <div>
                <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-2">
                  Notify Channel
                </p>
                <select
                  value={selectedUser.notify_identity_id?.toString() ?? ""}
                  onChange={(e) => void setNotifyIdentity(e.target.value)}
                  className="select select-bordered select-sm w-full text-sm"
                >
                  <option value="">Auto (first linked)</option>
                  {selectedUser.identities.map((ident: Identity) => (
                    <option key={ident.id} value={ident.id.toString()}>
                      {ident.platform}
                      {ident.name ? ` — ${ident.name}` : ` — ${ident.external_id}`}
                    </option>
                  ))}
                </select>
                <p className="text-xs text-muted-foreground mt-1">
                  Which channel receives scheduler and tool notifications.
                </p>
              </div>
            )}

            {/* Agent Assignments Section */}
            <div>
              <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-2">
                Agent Assignments
              </p>
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
                      onClick={() => void removeAgentFromUser(aid)}
                      className="text-destructive"
                    >
                      remove
                    </Button>
                  </div>
                ))}
                {userAgentIds.length === 0 && (
                  <div className="text-xs text-muted-foreground py-2">
                    No agent assignments (has access to all system-scope agents).
                  </div>
                )}
              </div>
              <div className="mt-3 flex gap-2">
                <select
                  value={addAgentId}
                  onChange={(e) => setAddAgentId(e.target.value)}
                  className="select select-bordered select-sm flex-1"
                >
                  <option value="">Select agent...</option>
                  {availableAgents.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name} ({a.id})
                    </option>
                  ))}
                </select>
                <Button size="sm" onClick={() => void addAgentToUser()} disabled={!addAgentId}>
                  Add
                </Button>
              </div>
            </div>

            {/* Metadata */}
            <div className="text-xs text-muted-foreground space-y-1 pt-2 border-t border-border">
              <p>Created: {selectedUser.created_at}</p>
              <p>Updated: {selectedUser.updated_at}</p>
            </div>
          </div>
        )}

        {/* Memory Tab */}
        {tab === "memory" && selectedLegacyUser && (
          <div className="space-y-4">
            {/* Default Agent */}
            <div className="flex items-center gap-3">
              <span className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider shrink-0">
                Default Agent
              </span>
              <select
                value={selectedLegacyUser._defaultAgent}
                onChange={(e) =>
                  setLegacyUsers((prev) =>
                    prev.map((lu) =>
                      lu.id === selectedLegacyUser.id
                        ? { ...lu, _defaultAgent: e.target.value }
                        : lu,
                    ),
                  )
                }
                className="select select-bordered select-sm flex-1 text-sm"
              >
                <option value="">None</option>
                {agents.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name} ({a.id})
                  </option>
                ))}
              </select>
              <Button
                size="sm"
                onClick={() => void saveUserDefaultAgent(selectedLegacyUser)}
                disabled={
                  selectedLegacyUser._defaultAgent === (selectedLegacyUser.default_agent_id || "")
                }
              >
                Save
              </Button>
            </div>

            {/* Memory entries */}
            <div>
              <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-3">
                Memory
                {selectedLegacyUser._memoryCount > 0 && (
                  <span className="text-primary ml-1">({selectedLegacyUser._memoryCount})</span>
                )}
              </p>
              <div className="space-y-4">
                {(userMemories[selectedLegacyUser.id] || []).map((mem) => (
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
                        setUserMemories((prev) => ({
                          ...prev,
                          [selectedLegacyUser.id]: (prev[selectedLegacyUser.id] || []).map((m) =>
                            m.agent_id === mem.agent_id ? { ...m, _content: e.target.value } : m,
                          ),
                        }))
                      }
                      rows={2}
                      className="w-full text-xs font-mono"
                    />
                    <div className="flex items-center gap-2 mt-1">
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() =>
                          confirmDelete(
                            `Delete memory for ${mem.agent_id}?`,
                            () => void doDeleteUserMemory(selectedLegacyUser.id, mem.agent_id),
                          )
                        }
                        className="text-muted-foreground hover:text-destructive"
                      >
                        delete
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() =>
                          void saveUserMemory(selectedLegacyUser.id, mem.agent_id, mem._content)
                        }
                        disabled={mem._content === mem.content}
                        className="text-primary disabled:opacity-30"
                      >
                        save
                      </Button>
                    </div>
                  </div>
                ))}
                {(userMemories[selectedLegacyUser.id] || []).length === 0 &&
                  !selectedLegacyUser._showAddMemory && (
                    <div className="text-xs text-muted-foreground py-2">No memories yet.</div>
                  )}
                {selectedLegacyUser._showAddMemory && (
                  <div className="space-y-2">
                    <select
                      value={selectedLegacyUser._newMemoryAgent}
                      onChange={(e) =>
                        setLegacyUsers((prev) =>
                          prev.map((lu) =>
                            lu.id === selectedLegacyUser.id
                              ? { ...lu, _newMemoryAgent: e.target.value }
                              : lu,
                          ),
                        )
                      }
                      className="select select-bordered select-sm w-full text-xs"
                    >
                      <option value="" disabled>
                        Select agent...
                      </option>
                      {agents
                        .filter(
                          (a) =>
                            !(userMemories[selectedLegacyUser.id] || []).some(
                              (m) => m.agent_id === a.id,
                            ),
                        )
                        .map((a) => (
                          <option key={a.id} value={a.id}>
                            {a.name} ({a.id})
                          </option>
                        ))}
                    </select>
                    <Textarea
                      value={selectedLegacyUser._newMemoryContent}
                      onChange={(e) =>
                        setLegacyUsers((prev) =>
                          prev.map((lu) =>
                            lu.id === selectedLegacyUser.id
                              ? { ...lu, _newMemoryContent: e.target.value }
                              : lu,
                          ),
                        )
                      }
                      rows={2}
                      placeholder="Memory content..."
                      className="w-full text-xs font-mono"
                    />
                    <div className="flex gap-2">
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() =>
                          setLegacyUsers((prev) =>
                            prev.map((lu) =>
                              lu.id === selectedLegacyUser.id
                                ? { ...lu, _showAddMemory: false }
                                : lu,
                            ),
                          )
                        }
                        className="text-muted-foreground"
                      >
                        cancel
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => void addUserMemory(selectedLegacyUser)}
                        disabled={
                          !selectedLegacyUser._newMemoryAgent ||
                          !selectedLegacyUser._newMemoryContent
                        }
                        className="text-primary disabled:opacity-30"
                      >
                        add
                      </Button>
                    </div>
                  </div>
                )}
                {!selectedLegacyUser._showAddMemory && (
                  <button
                    onClick={() =>
                      setLegacyUsers((prev) =>
                        prev.map((lu) =>
                          lu.id === selectedLegacyUser.id
                            ? {
                                ...lu,
                                _showAddMemory: true,
                                _newMemoryAgent: "",
                                _newMemoryContent: "",
                              }
                            : lu,
                        ),
                      )
                    }
                    className="text-xs text-primary hover:text-primary cursor-pointer"
                  >
                    + Add memory
                  </button>
                )}
              </div>
            </div>
          </div>
        )}

        {/* Memory Tab — legacy user not yet loaded */}
        {tab === "memory" && !selectedLegacyUser && (
          <div className="text-xs text-muted-foreground py-4">Loading...</div>
        )}
      </div>
    </div>
  ) : undefined;

  return (
    <div className="-my-8 -mx-10 h-[calc(100%+4rem)]">
      <SettingsDetailLayout
        listHeader={listHeader}
        list={list}
        detail={detail}
        emptyState={
          <p className="text-sm text-muted-foreground">
            Select a user to manage their agents and memory.
          </p>
        }
      />

      {/* Confirm Dialog */}
      <Dialog
        open={!!confirm}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
      >
        <DialogPopup className="max-w-sm" showCloseButton={false}>
          <DialogPanel>
            <p className="text-sm">{confirm?.message}</p>
          </DialogPanel>
          <DialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setConfirm(null)}>
              {t("common.cancel")}
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                confirm?.onConfirm();
                setConfirm(null);
              }}
            >
              {t("common.confirm")}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>

      {/* Toast */}
      {toast && (
        <div className="fixed bottom-4 right-4 z-[9999]">
          <div
            className={`rounded-lg border text-sm py-2 px-4 shadow-lg ${
              toast.type === "error"
                ? "border-destructive/36 bg-destructive/8 text-destructive-foreground"
                : "border-success/36 bg-success/8 text-success-foreground"
            }`}
          >
            <span>{toast.message}</span>
          </div>
        </div>
      )}
    </div>
  );
}
