import { useEffect, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { onboardingQueryOptions } from "@/lib/queries/onboarding";
import { createWorkspace, redeemInviteOnboarding, getInviteInfo } from "@/lib/api-client/sdk.gen";
import type { OnboardingStatus } from "@/lib/api-client/types.gen";

export function OnboardingPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: status, isLoading } = useQuery(onboardingQueryOptions);

  async function onSuccess() {
    await queryClient.invalidateQueries({ queryKey: ["me"] });
    await queryClient.invalidateQueries({ queryKey: ["onboarding"] });
    void navigate({ to: "/" });
  }

  if (isLoading) {
    return (
      <Shell>
        <div className="h-4 w-4 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
      </Shell>
    );
  }

  if (!status) {
    return (
      <Shell>
        <p className="text-sm text-muted-foreground">Unable to load onboarding status.</p>
      </Shell>
    );
  }

  return (
    <Shell>
      <div className="text-center">
        <span className="font-serif italic text-primary text-3xl tracking-tight select-none">
          stella
        </span>
        <p className="text-muted-foreground text-sm mt-1">Welcome, {status.name || status.email}</p>
      </div>

      {status.invite_token && (
        <InviteTokenSection token={status.invite_token} onSuccess={onSuccess} />
      )}

      {status.pending_invites && status.pending_invites.length > 0 && (
        <PendingInvitesSection invites={status.pending_invites} onSuccess={onSuccess} />
      )}

      <CreateWorkspaceSection onSuccess={onSuccess} />

      <PasteInviteSection onSuccess={onSuccess} />
    </Shell>
  );
}

function InviteTokenSection({ token, onSuccess }: { token: string; onSuccess: () => void }) {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [inviteInfo, setInviteInfo] = useState<{ org_name: string; role: string } | null>(null);

  useEffect(() => {
    let cancelled = false;
    void getInviteInfo({ path: { token } }).then((res) => {
      if (cancelled) return;
      if (res.data) {
        const data = (res.data as { data?: { org_name?: string; invite?: { role?: string } } })
          .data;
        setInviteInfo({
          org_name: data?.org_name ?? "Unknown",
          role: data?.invite?.role ?? "user",
        });
      }
    });
    return () => {
      cancelled = true;
    };
  }, [token]);

  async function handleJoin() {
    setLoading(true);
    setError(null);
    const res = await redeemInviteOnboarding({ body: { token } });
    if (res.error) {
      setError("Failed to accept invite. It may have expired.");
      setLoading(false);
      return;
    }
    onSuccess();
  }

  return (
    <Section title="You've been invited">
      {inviteInfo ? (
        <p className="text-sm text-muted-foreground">
          Join <span className="font-medium text-foreground">{inviteInfo.org_name}</span> as{" "}
          <span className="font-medium text-foreground">{inviteInfo.role}</span>
        </p>
      ) : (
        <p className="text-sm text-muted-foreground">Loading invite details...</p>
      )}
      {error && <p className="text-sm text-destructive">{error}</p>}
      <Button onClick={handleJoin} disabled={loading} className="w-full">
        {loading ? "Joining..." : "Join Organization"}
      </Button>
    </Section>
  );
}

function PendingInvitesSection({
  invites,
  onSuccess,
}: {
  invites: NonNullable<OnboardingStatus["pending_invites"]>;
  onSuccess: () => void;
}) {
  const [loading, setLoading] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function handleAccept(inviteId: string) {
    setLoading(inviteId);
    setError(null);
    const res = await redeemInviteOnboarding({ body: { invite_id: inviteId } });
    if (res.error) {
      setError("Failed to accept invite.");
      setLoading(null);
      return;
    }
    onSuccess();
  }

  return (
    <Section title="Pending Invites">
      {error && <p className="text-sm text-destructive">{error}</p>}
      <div className="space-y-2">
        {invites.map((inv) => (
          <div key={inv.id} className="flex items-center justify-between rounded-lg border p-3">
            <div>
              <p className="text-sm font-medium">{inv.org_name}</p>
              <p className="text-xs text-muted-foreground">Role: {inv.role}</p>
            </div>
            <Button size="sm" onClick={() => handleAccept(inv.id)} disabled={loading === inv.id}>
              {loading === inv.id ? "..." : "Accept"}
            </Button>
          </div>
        ))}
      </div>
    </Section>
  );
}

function CreateWorkspaceSection({ onSuccess }: { onSuccess: () => void }) {
  const [name, setName] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleCreate() {
    setLoading(true);
    setError(null);
    const res = await createWorkspace({ body: { name: name.trim() || undefined } });
    if (res.error) {
      setError("Failed to create workspace.");
      setLoading(false);
      return;
    }
    onSuccess();
  }

  return (
    <Section title="Create a Workspace">
      <Input
        placeholder="Workspace name (optional)"
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      {error && <p className="text-sm text-destructive">{error}</p>}
      <Button onClick={handleCreate} disabled={loading} className="w-full">
        {loading ? "Creating..." : "Create Workspace"}
      </Button>
    </Section>
  );
}

function PasteInviteSection({ onSuccess }: { onSuccess: () => void }) {
  const [token, setToken] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleRedeem() {
    if (!token.trim()) return;
    setLoading(true);
    setError(null);
    const res = await redeemInviteOnboarding({ body: { token: token.trim() } });
    if (res.error) {
      setError("Invalid or expired invite code.");
      setLoading(false);
      return;
    }
    onSuccess();
  }

  return (
    <Section title="Have an Invite Code?">
      <Input
        placeholder="Paste invite code"
        value={token}
        onChange={(e) => setToken(e.target.value)}
      />
      {error && <p className="text-sm text-destructive">{error}</p>}
      <Button
        variant="outline"
        onClick={handleRedeem}
        disabled={loading || !token.trim()}
        className="w-full"
      >
        {loading ? "Joining..." : "Join with Code"}
      </Button>
    </Section>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="w-full space-y-3 rounded-lg border border-border p-4">
      <h3 className="text-sm font-semibold">{title}</h3>
      {children}
    </div>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <main className="flex min-h-screen items-center justify-center bg-background text-foreground">
      <div className="flex w-full max-w-md flex-col items-center gap-6 rounded-xl border bg-card p-8 shadow-sm">
        {children}
      </div>
    </main>
  );
}
