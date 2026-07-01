import { useCallback, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  listPersonalAccessTokens,
  listTokenScopes,
  createPersonalAccessToken,
  revokePersonalAccessToken,
} from "@/lib/api-client/sdk.gen";
import type { PersonalAccessToken } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";

type Notify = (message: string, type?: "success" | "error") => void;

function tokenStatus(t: PersonalAccessToken): "active" | "revoked" | "expired" {
  if (t.revoked_at) return "revoked";
  if (t.expires_at && new Date(t.expires_at).getTime() <= Date.now()) return "expired";
  return "active";
}

export function TokensSection({ notify }: { notify: Notify }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();

  const [name, setName] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [plaintext, setPlaintext] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const tokensQuery = useQuery({
    queryKey: ["personalAccessTokens"],
    queryFn: async () => {
      const { data } = await listPersonalAccessTokens({ throwOnError: true });
      return data;
    },
  });
  const scopesQuery = useQuery({
    queryKey: ["personalAccessTokenScopes"],
    queryFn: async () => {
      const { data } = await listTokenScopes({ throwOnError: true });
      return data;
    },
  });

  const tokens = tokensQuery.data?.tokens ?? [];
  const scopeCatalog = useMemo(() => scopesQuery.data?.scopes ?? [], [scopesQuery.data]);

  const toggleScope = useCallback((id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const create = useMutation({
    mutationFn: async () => {
      const { data } = await createPersonalAccessToken({
        body: { name: name.trim(), scopes: [...selected] },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: (data) => {
      setPlaintext(data?.token ?? null);
      setName("");
      setSelected(new Set());
      void queryClient.invalidateQueries({ queryKey: ["personalAccessTokens"] });
      notify(t("account.tokens.created"));
    },
    onError: (e) =>
      notify(e instanceof Error ? e.message : t("account.tokens.createFailed"), "error"),
  });

  const revoke = useMutation({
    mutationFn: async (id: string) => {
      await revokePersonalAccessToken({ path: { id }, throwOnError: true });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["personalAccessTokens"] });
      notify(t("account.tokens.revoked"));
    },
    onError: (e) =>
      notify(e instanceof Error ? e.message : t("account.tokens.revokeFailed"), "error"),
  });

  const onCreate = useCallback(() => {
    if (!name.trim()) {
      notify(t("account.tokens.needName"), "error");
      return;
    }
    if (selected.size === 0) {
      notify(t("account.tokens.needScope"), "error");
      return;
    }
    create.mutate();
  }, [name, selected, create, notify, t]);

  const copyPlaintext = useCallback(async () => {
    if (!plaintext) return;
    await navigator.clipboard.writeText(plaintext);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }, [plaintext]);

  return (
    <section>
      <h2 className="text-base font-semibold text-foreground mb-3">{t("account.tokens")}</h2>
      <div className="rounded-xl border border-border bg-card p-6 space-y-6">
        <p className="text-sm text-muted-foreground">{t("account.tokens.description")}</p>

        {/* One-time plaintext reveal */}
        {plaintext && (
          <div className="rounded-lg border border-border bg-muted/50 p-4 space-y-3">
            <p className="text-sm font-medium text-foreground">{t("account.tokens.copyOnce")}</p>
            <div className="flex items-center gap-2">
              <code className="flex-1 truncate rounded-md bg-background px-3 py-2 font-mono text-sm">
                {plaintext}
              </code>
              <Button size="sm" variant="outline" onClick={copyPlaintext}>
                {copied ? t("account.tokens.copied") : t("account.tokens.copy")}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setPlaintext(null)}>
                {t("account.tokens.done")}
              </Button>
            </div>
          </div>
        )}

        {/* Create form */}
        <div className="space-y-4">
          <div className="space-y-1.5">
            <label className="block text-xs font-medium text-muted-foreground">
              {t("account.tokens.name")}
            </label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("account.tokens.namePlaceholder")}
              nativeInput
            />
          </div>
          <div className="space-y-1.5">
            <label className="block text-xs font-medium text-muted-foreground">
              {t("account.tokens.scopes")}
            </label>
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              {scopeCatalog.map((s) => (
                <label
                  key={s.id}
                  className="flex items-start gap-2 rounded-md border border-border bg-background px-3 py-2 text-sm"
                >
                  <input
                    type="checkbox"
                    className="mt-0.5"
                    checked={selected.has(s.id)}
                    onChange={() => toggleScope(s.id)}
                  />
                  <span className="flex flex-col">
                    <span className="font-mono text-xs text-foreground">{s.id}</span>
                    <span className="text-xs text-muted-foreground">{s.description}</span>
                  </span>
                </label>
              ))}
            </div>
          </div>
          <div className="flex justify-end">
            <Button size="sm" loading={create.isPending} onClick={onCreate}>
              {t("account.tokens.create")}
            </Button>
          </div>
        </div>

        {/* Existing tokens */}
        <div className="space-y-3">
          {tokens.length === 0 ? (
            <p className="py-2 text-center text-sm text-muted-foreground">
              {t("account.tokens.none")}
            </p>
          ) : (
            tokens.map((tok) => {
              const status = tokenStatus(tok);
              return (
                <div
                  key={tok.id}
                  className="flex items-center justify-between rounded-lg border border-border bg-background px-4 py-3"
                >
                  <div className="flex min-w-0 flex-col gap-1">
                    <span className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium text-foreground">
                        {tok.name}
                      </span>
                      <span className="font-mono text-xs text-muted-foreground">
                        ...{tok.last4}
                      </span>
                      <Badge size="sm" variant={status === "active" ? "outline" : "secondary"}>
                        {status === "active"
                          ? t("account.tokens.statusActive")
                          : status === "revoked"
                            ? t("account.tokens.statusRevoked")
                            : t("account.tokens.statusExpired")}
                      </Badge>
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {tok.scopes.join(", ") || "—"}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {t("account.tokens.expires")}:{" "}
                      {tok.expires_at
                        ? new Date(tok.expires_at).toLocaleDateString()
                        : t("account.tokens.never")}
                    </span>
                  </div>
                  {status !== "revoked" && (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="shrink-0 text-destructive hover:bg-destructive/10"
                      loading={revoke.isPending}
                      onClick={() => revoke.mutate(tok.id)}
                    >
                      {t("account.tokens.revoke")}
                    </Button>
                  )}
                </div>
              );
            })
          )}
        </div>
      </div>
    </section>
  );
}
