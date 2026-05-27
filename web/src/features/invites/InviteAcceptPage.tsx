import { useEffect, useState } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { acceptInvite, getInviteInfo } from "@/lib/api-client/sdk.gen";

export function InviteAcceptPage() {
  const { token } = useParams({ strict: false }) as { token: string };
  const navigate = useNavigate();
  const [orgName, setOrgName] = useState<string | null>(null);
  const [role, setRole] = useState<string>("");
  const [error, setError] = useState<string | null>(null);
  const [accepting, setAccepting] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void getInviteInfo({ path: { token } }).then((res) => {
      if (cancelled) return;
      if (res.error || !res.data) {
        setError("Invite not found or expired");
        return;
      }
      const data = (res.data as { data?: { org_name?: string; invite?: { role?: string } } }).data;
      setOrgName(data?.org_name ?? "Unknown");
      setRole(data?.invite?.role ?? "user");
    });
    return () => {
      cancelled = true;
    };
  }, [token]);

  async function handleAccept() {
    setAccepting(true);
    const res = await acceptInvite({ path: { token } });
    if (res.error) {
      setError("Failed to accept invite");
      setAccepting(false);
      return;
    }
    void navigate({ to: "/" });
  }

  if (error) {
    return (
      <Shell>
        <p className="text-sm text-muted-foreground">{error}</p>
        <Button variant="outline" onClick={() => navigate({ to: "/" })}>
          Go Home
        </Button>
      </Shell>
    );
  }

  if (!orgName) {
    return (
      <Shell>
        <div className="h-4 w-4 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
      </Shell>
    );
  }

  return (
    <Shell>
      <h2 className="text-lg font-semibold">You've been invited</h2>
      <p className="text-sm text-muted-foreground">
        Join <span className="font-medium text-foreground">{orgName}</span> as{" "}
        <span className="font-medium text-foreground">{role}</span>.
      </p>
      <p className="text-xs text-muted-foreground">
        Accepting this invite will move you to this organization.
      </p>
      <div className="flex gap-3">
        <Button onClick={handleAccept} disabled={accepting}>
          {accepting ? "Accepting..." : "Accept Invite"}
        </Button>
        <Button variant="outline" onClick={() => navigate({ to: "/" })}>
          Decline
        </Button>
      </div>
    </Shell>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <main className="flex min-h-screen items-center justify-center bg-background text-foreground">
      <div className="flex flex-col items-center gap-4 rounded-xl border bg-card p-8 shadow-sm">
        {children}
      </div>
    </main>
  );
}
