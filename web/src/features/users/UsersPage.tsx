import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { Agent, Identity, User, UserMemory } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogHeader,
  DialogPanel,
  DialogFooter,
} from "@/components/ui/dialog";

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
      setLegacyUsers(
        list.map((u) => ({
          ...u,
          _defaultAgent: u.default_agent_id || "",
          _showMemory: false,
          _memoryCount: 0,
          _showAddMemory: false,
          _newMemoryAgent: "",
          _newMemoryContent: "",
        })),
      );
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

  const toggleUserMemory = useCallback(
    async (u: LegacyUser) => {
      const show = !u._showMemory;
      setLegacyUsers((prev) =>
        prev.map((lu) => (lu.id === u.id ? { ...lu, _showMemory: show } : lu)),
      );
      if (show) await loadUserMemories(u.id);
    },
    [loadUserMemories],
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

  const availableAgents = agents.filter((a) => !userAgentIds.includes(a.id));

  return (
    <div>
      {/* Page header */}
      <div className="mb-6">
        <h1 className="font-serif text-2xl font-normal tracking-tight mb-1">User management</h1>
        <p className="text-sm text-muted-foreground">
          Manage auth users, roles, agent assignments, and linked identities.
        </p>
      </div>

      {/* Tabs */}
      <div className="flex gap-4 border-b border-border mb-6">
        <button
          onClick={() => setTab("auth")}
          className={`pb-2 text-sm font-medium transition-colors border-b-2 ${
            tab === "auth"
              ? "border-primary text-primary"
              : "border-transparent text-muted-foreground hover:text-foreground"
          }`}
        >
          Auth Users
        </button>
        <button
          onClick={() => setTab("memory")}
          className={`pb-2 text-sm font-medium transition-colors border-b-2 ${
            tab === "memory"
              ? "border-primary text-primary"
              : "border-transparent text-muted-foreground hover:text-foreground"
          }`}
        >
          User Memory
        </button>
      </div>

      {/* Auth Users Tab */}
      {tab === "auth" && (
        <div>
          <div className="text-xs font-mono text-muted-foreground mb-4">
            {authUsers.length} users
          </div>
          <div className="border-t border-border divide-y divide-border">
            {authUsers.map((u) => (
              <div key={u.id} className="py-4">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3">
                    <button
                      onClick={() => void selectUser(u)}
                      className="font-medium hover:text-primary transition-colors cursor-pointer"
                    >
                      {u.username}
                    </button>
                    <span className="text-xs font-mono text-muted-foreground">#{u.id}</span>
                    <Badge variant={u.role === "admin" ? "default" : "outline"} size="sm">
                      {u.role}
                    </Badge>
                    {!u.is_active && (
                      <Badge variant="error" size="sm">
                        inactive
                      </Badge>
                    )}
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-muted-foreground">{u.created_at}</span>
                  </div>
                </div>
                {u.identities && u.identities.length > 0 && (
                  <div className="mt-1 flex items-center gap-2">
                    <span className="text-xs text-muted-foreground">Linked:</span>
                    {u.identities.map((ident) => (
                      <Badge
                        key={ident.id}
                        variant="outline"
                        size="sm"
                        className="font-mono uppercase"
                      >
                        {ident.platform}
                      </Badge>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
          {authUsers.length === 0 && (
            <div className="py-12 text-center text-sm text-muted-foreground">
              No users registered yet.
            </div>
          )}
        </div>
      )}

      {/* Memory Tab */}
      {tab === "memory" && (
        <div>
          <div className="text-xs font-mono text-muted-foreground mb-4">
            {legacyUsers.length} users
          </div>
          <div className="border-t border-border divide-y divide-border">
            {legacyUsers.map((u) => (
              <div key={u.id} className="py-6">
                <div className="flex items-baseline justify-between mb-3">
                  <div className="flex items-baseline gap-3">
                    <span className="font-medium">{u.username || "Unnamed"}</span>
                  </div>
                  <span className="text-xs font-mono text-muted-foreground">#{u.id}</span>
                </div>
                <div className="flex items-center gap-3 mb-3">
                  <span className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider shrink-0">
                    Default Agent
                  </span>
                  <select
                    value={u._defaultAgent}
                    onChange={(e) =>
                      setLegacyUsers((prev) =>
                        prev.map((lu) =>
                          lu.id === u.id ? { ...lu, _defaultAgent: e.target.value } : lu,
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
                    onClick={() => void saveUserDefaultAgent(u)}
                    disabled={u._defaultAgent === (u.default_agent_id || "")}
                  >
                    Save
                  </Button>
                </div>
                <button
                  onClick={() => void toggleUserMemory(u)}
                  className="text-xs text-muted-foreground hover:text-primary transition-colors cursor-pointer flex items-center gap-1"
                >
                  <span>{u._showMemory ? "▾" : "▸"}</span>
                  <span>Memory</span>
                  {u._memoryCount > 0 && (
                    <span className="font-mono text-primary">({u._memoryCount})</span>
                  )}
                </button>
                {u._showMemory && (
                  <div className="mt-4 pl-4 border-l-2 border-border space-y-4">
                    {(userMemories[u.id] || []).map((mem) => (
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
                              [u.id]: (prev[u.id] || []).map((m) =>
                                m.agent_id === mem.agent_id
                                  ? { ...m, _content: e.target.value }
                                  : m,
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
                                () => void doDeleteUserMemory(u.id, mem.agent_id),
                              )
                            }
                            className="text-muted-foreground hover:text-destructive"
                          >
                            delete
                          </Button>
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => void saveUserMemory(u.id, mem.agent_id, mem._content)}
                            disabled={mem._content === mem.content}
                            className="text-primary disabled:opacity-30"
                          >
                            save
                          </Button>
                        </div>
                      </div>
                    ))}
                    {(userMemories[u.id] || []).length === 0 && !u._showAddMemory && (
                      <div className="text-xs text-muted-foreground py-2">No memories yet.</div>
                    )}
                    {u._showAddMemory && (
                      <div className="space-y-2">
                        <select
                          value={u._newMemoryAgent}
                          onChange={(e) =>
                            setLegacyUsers((prev) =>
                              prev.map((lu) =>
                                lu.id === u.id ? { ...lu, _newMemoryAgent: e.target.value } : lu,
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
                              (a) => !(userMemories[u.id] || []).some((m) => m.agent_id === a.id),
                            )
                            .map((a) => (
                              <option key={a.id} value={a.id}>
                                {a.name} ({a.id})
                              </option>
                            ))}
                        </select>
                        <Textarea
                          value={u._newMemoryContent}
                          onChange={(e) =>
                            setLegacyUsers((prev) =>
                              prev.map((lu) =>
                                lu.id === u.id ? { ...lu, _newMemoryContent: e.target.value } : lu,
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
                                  lu.id === u.id ? { ...lu, _showAddMemory: false } : lu,
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
                            onClick={() => void addUserMemory(u)}
                            disabled={!u._newMemoryAgent || !u._newMemoryContent}
                            className="text-primary disabled:opacity-30"
                          >
                            add
                          </Button>
                        </div>
                      </div>
                    )}
                    {!u._showAddMemory && (
                      <button
                        onClick={() =>
                          setLegacyUsers((prev) =>
                            prev.map((lu) =>
                              lu.id === u.id
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
                )}
              </div>
            ))}
          </div>
          {legacyUsers.length === 0 && (
            <div className="py-12 text-center text-sm text-muted-foreground">
              No channel users yet — they appear after someone messages Stella.
            </div>
          )}
        </div>
      )}

      {/* User Detail Modal */}
      <Dialog
        open={!!selectedUser}
        onOpenChange={(open) => {
          if (!open && !confirm) setSelectedUser(null);
        }}
      >
        <DialogPopup className="max-w-lg max-h-[90vh]">
          <DialogHeader>
            <DialogTitle>{selectedUser?.username}</DialogTitle>
          </DialogHeader>
          <DialogPanel>
            {selectedUser && (
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
                    className={
                      selectedUser.is_active ? "text-destructive" : "text-success-foreground"
                    }
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
                        title={
                          selectedUser.id === currentUserId ? "Cannot demote yourself" : undefined
                        }
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
                      <div className="text-xs text-muted-foreground py-2">
                        No linked identities.
                      </div>
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
          </DialogPanel>
        </DialogPopup>
      </Dialog>

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
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                confirm?.onConfirm();
                setConfirm(null);
              }}
            >
              Confirm
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
