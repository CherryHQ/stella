import { ExternalLink } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { MarketCard } from "@/features/marketplace/MarketCard";
import { targetValue } from "@/lib/utils";
import type { McpRegistryServer } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";

/** The auth badge label key for one registry auth classification. */
export function registryAuthLabelKey(auth: string) {
  // SAFETY: the auth values are a closed backend enum; an unknown value renders
  // as the generic label rather than inventing one.
  if (auth === "bearer") return "mcp.market.auth.bearer";
  if (auth === "unsupported") return "mcp.market.auth.unsupported";
  return "mcp.market.auth.none";
}

export function RegistryAuthBadge({ auth }: { auth: string }) {
  const { t } = useI18n();
  return (
    <Badge variant="outline" size="sm">
      {t(registryAuthLabelKey(auth))}
    </Badge>
  );
}

export function RegistryCard({
  server,
  installing,
  installDisabled,
  onOpen,
  onInstall,
}: {
  server: McpRegistryServer;
  installing: boolean;
  installDisabled: boolean;
  onOpen: () => void;
  onInstall: () => void;
}) {
  return (
    <MarketCard
      title={server.name}
      version={server.version ?? null}
      description={server.description ?? null}
      badge={<RegistryAuthBadge auth={server.auth} />}
      footerMeta={
        server.transport ? <span className="font-mono text-xs">{server.transport}</span> : null
      }
      installed={false}
      installing={installing}
      installDisabled={installDisabled}
      onOpen={onOpen}
      onInstall={onInstall}
    />
  );
}

/**
 * The marketplace detail pane: description, repository link, connection URL,
 * and the header requirements. `unsupported` entries list the headers the user
 * must configure manually before the server can work.
 */
export function RegistryDetailBody({ server }: { server: McpRegistryServer }) {
  const { t } = useI18n();
  const headers = server.headers ?? [];
  return (
    <div className="space-y-4">
      {server.description && <p className="text-sm text-muted-foreground">{server.description}</p>}
      <div className="flex flex-wrap items-center gap-2">
        <RegistryAuthBadge auth={server.auth} />
        {server.version && (
          <Badge variant="outline" size="sm">
            v{server.version}
          </Badge>
        )}
        {server.transport && (
          <Badge variant="outline" size="sm">
            {server.transport}
          </Badge>
        )}
      </div>
      <div className="space-y-1.5">
        <p className="text-xs font-medium text-muted-foreground">{t("mcp.market.url")}</p>
        <p className="truncate font-mono text-sm">{server.url}</p>
      </div>
      {headers.length > 0 && (
        <div className="space-y-2">
          <p className="text-xs font-medium text-muted-foreground">{t("mcp.market.headers")}</p>
          {headers.map((h) => (
            <div key={h.name} className="rounded-lg border p-3">
              <div className="flex items-center gap-2">
                <span className="font-mono text-sm font-medium">{h.name}</span>
                {h.required && (
                  <Badge variant="warning" size="sm">
                    {t("mcp.market.headerRequired")}
                  </Badge>
                )}
                {h.is_secret && (
                  <Badge variant="outline" size="sm">
                    {t("mcp.market.headerSecret")}
                  </Badge>
                )}
              </div>
              {h.template != null && (
                <p className="mt-1 truncate font-mono text-xs text-muted-foreground">
                  {h.template}
                </p>
              )}
              {h.description && (
                <p className="mt-1 text-xs text-muted-foreground">{h.description}</p>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export function RegistryRepositoryLink({ server }: { server: McpRegistryServer }) {
  if (!server.repository) return null;
  let host = server.repository;
  try {
    host = new URL(server.repository).host;
  } catch {
    // A malformed repository URL still renders as a link with the raw text.
  }
  return (
    <Button
      size="sm"
      variant="ghost"
      render={<a href={server.repository} target="_blank" rel="noreferrer" />}
    >
      <ExternalLink size={16} />
      {host}
    </Button>
  );
}

/**
 * The secret capture step for bearer entries: the registry's header template
 * (e.g. "Bearer {smithery_api_key}") is the label; the captured value becomes
 * the registration's vault-stored bearer token.
 */
export function BearerSecretStep({
  server,
  value,
  onChange,
}: {
  server: McpRegistryServer;
  value: string;
  onChange: (v: string) => void;
}) {
  const { t } = useI18n();
  const header = (server.headers ?? []).find((h) => h.name.toLowerCase() === "authorization");
  return (
    <Field>
      <FieldLabel>
        {t("mcp.market.bearerFor", { template: header?.template ?? "{token}" })}
      </FieldLabel>
      <Input
        type="password"
        value={value}
        onChange={(e) => onChange(targetValue(e))}
        autoComplete="off"
        nativeInput
      />
      <FieldDescription>{t("mcp.market.bearerHint")}</FieldDescription>
    </Field>
  );
}
