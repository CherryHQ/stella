import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { createInvite, listInvites, revokeInvite } from "@/lib/api-client/sdk.gen";
import { useToast, ToastContainer } from "@/hooks/use-toast";

interface Invite {
  id: string;
  email?: string;
  role: string;
  status: string;
  max_uses: number;
  use_count: number;
  expires_at: string;
}

export function InviteManagement() {
  const [invites, setInvites] = useState<Invite[]>([]);
  const [showCreate, setShowCreate] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("user");
  const [creating, setCreating] = useState(false);
  const [inviteURL, setInviteURL] = useState<string | null>(null);
  const { toasts, showToast } = useToast();

  const loadInvites = useCallback(async () => {
    const res = await listInvites();
    if (res.data) {
      const data = res.data as { data?: { items?: Invite[] } };
      setInvites(data.data?.items ?? []);
    }
  }, []);

  useEffect(() => {
    void loadInvites();
  }, [loadInvites]);

  async function handleCreate() {
    setCreating(true);
    const res = await createInvite({
      body: { role: role as "admin" | "user", email: email || undefined, ttl_hours: 168 },
    });
    setCreating(false);
    if (res.error) {
      showToast("Failed to create invite", "error");
      return;
    }
    const data = (res.data as { data?: { invite_url?: string } })?.data;
    if (data?.invite_url) {
      setInviteURL(data.invite_url);
    }
    setEmail("");
    setShowCreate(false);
    void loadInvites();
  }

  async function handleRevoke(id: string) {
    await revokeInvite({ path: { id } });
    showToast("Invite revoked");
    void loadInvites();
  }

  async function copyURL(url: string) {
    await navigator.clipboard.writeText(url);
    showToast("Copied to clipboard");
  }

  return (
    <div className="flex flex-col gap-4 p-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">Invites</h3>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          Invite User
        </Button>
      </div>

      {showCreate && (
        <div className="flex flex-col gap-2 rounded-lg border bg-card p-3">
          <input
            className="rounded border bg-background px-2 py-1 text-sm"
            placeholder="Email (optional)"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <select
            className="rounded border bg-background px-2 py-1 text-sm"
            value={role}
            onChange={(e) => setRole(e.target.value)}
          >
            <option value="user">User</option>
            <option value="admin">Admin</option>
          </select>
          <div className="flex gap-2">
            <Button size="sm" onClick={handleCreate} disabled={creating}>
              {creating ? "Creating..." : "Create"}
            </Button>
            <Button size="sm" variant="outline" onClick={() => setShowCreate(false)}>
              Cancel
            </Button>
          </div>
        </div>
      )}

      {inviteURL && (
        <div className="flex items-center gap-2 rounded-lg border bg-muted p-3">
          <code className="flex-1 truncate text-xs">{inviteURL}</code>
          <Button size="sm" variant="outline" onClick={() => void copyURL(inviteURL)}>
            Copy
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setInviteURL(null)}>
            Dismiss
          </Button>
        </div>
      )}

      {invites.length > 0 && (
        <div className="flex flex-col gap-1">
          {invites.map((inv) => (
            <div key={inv.id} className="flex items-center gap-2 rounded px-2 py-1.5 text-sm">
              <span className="flex-1 truncate">{inv.email || "Anyone"}</span>
              <Badge variant="outline" size="sm">
                {inv.role}
              </Badge>
              <Badge variant={inv.status === "pending" ? "default" : "outline"} size="sm">
                {inv.status}
              </Badge>
              <span className="text-xs text-muted-foreground">
                {inv.use_count}/{inv.max_uses}
              </span>
              {inv.status === "pending" && (
                <Button size="sm" variant="ghost" onClick={() => void handleRevoke(inv.id)}>
                  Revoke
                </Button>
              )}
            </div>
          ))}
        </div>
      )}

      <ToastContainer messages={toasts} />
    </div>
  );
}
