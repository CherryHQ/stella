import { useCallback, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  listPersonalAccessTokens,
  createPersonalAccessToken,
  revokePersonalAccessToken,
} from "@/lib/api-client/sdk.gen";
import type { PersonalAccessToken } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Field, FieldLabel } from "@/components/ui/field";
import { Form } from "@/components/ui/form";

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
  const [plaintext, setPlaintext] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const tokensQuery = useQuery({
    queryKey: ["personalAccessTokens"],
    queryFn: async () => {
      const { data } = await listPersonalAccessTokens({ throwOnError: true });
      return data;
    },
  });

  const tokens = tokensQuery.data?.tokens ?? [];

  const create = useMutation({
    mutationFn: async () => {
      const { data } = await createPersonalAccessToken({
        body: { name: name.trim() },
        throwOnError: true,
      });
      return data;
    },
    onSuccess: (data) => {
      setPlaintext(data?.token ?? null);
      setName("");
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
    create.mutate();
  }, [name, create, notify, t]);

  const copyPlaintext = useCallback(async () => {
    if (!plaintext) return;
    await navigator.clipboard.writeText(plaintext);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }, [plaintext]);

  return (
    <section>
      <h2 className="text-base font-semibold text-foreground mb-3">{t("account.tokens")}</h2>
      <Card>
        <CardContent className="space-y-6">
          <p className="text-sm text-muted-foreground">{t("account.tokens.description")}</p>

          {plaintext && (
            <Card>
              <CardContent className="space-y-3">
                <p className="text-sm font-medium text-foreground">
                  {t("account.tokens.copyOnce")}
                </p>
                <Input
                  value={plaintext}
                  type="text"
                  readOnly
                  nativeInput
                  aria-label={t("account.tokens")}
                />
                <div className="flex gap-2">
                  <Button size="sm" variant="outline" onClick={copyPlaintext}>
                    {copied ? t("account.tokens.copied") : t("account.tokens.copy")}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setPlaintext(null)}>
                    {t("account.tokens.done")}
                  </Button>
                </div>
              </CardContent>
            </Card>
          )}

          <Form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              onCreate();
            }}
          >
            <Field>
              <FieldLabel>{t("account.tokens.name")}</FieldLabel>
              <Input
                name="name"
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("account.tokens.namePlaceholder")}
              />
            </Field>
            <div className="flex justify-end">
              <Button type="submit" size="sm" loading={create.isPending}>
                {t("account.tokens.create")}
              </Button>
            </div>
          </Form>

          <div className="space-y-3">
            {tokens.length === 0 ? (
              <p className="py-2 text-center text-sm text-muted-foreground">
                {t("account.tokens.none")}
              </p>
            ) : (
              tokens.map((tok) => {
                const status = tokenStatus(tok);
                return (
                  <Card key={tok.id}>
                    <CardContent className="flex flex-col items-stretch gap-4 sm:flex-row sm:items-center sm:justify-between">
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
                          {t("account.tokens.expires")}:{" "}
                          {tok.expires_at
                            ? new Date(tok.expires_at).toLocaleDateString()
                            : t("account.tokens.never")}
                        </span>
                      </div>
                      {status !== "revoked" && (
                        <Button
                          size="sm"
                          variant="destructive-outline"
                          loading={revoke.isPending}
                          onClick={() => revoke.mutate(tok.id)}
                        >
                          {t("account.tokens.revoke")}
                        </Button>
                      )}
                    </CardContent>
                  </Card>
                );
              })
            )}
          </div>
        </CardContent>
      </Card>
    </section>
  );
}
