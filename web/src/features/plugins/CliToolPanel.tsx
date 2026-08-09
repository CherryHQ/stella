import { useEffect, useRef, useState } from "react";
import { getCliToolLatest, searchCliToolRegistry } from "@/lib/api-client/sdk.gen";
import type { CliToolRegistryItem } from "@/lib/api-client/types.gen";
import type {
  ManifestBinary,
  ManifestOAuthProvider,
  ManifestPlugin,
  ManifestSessionEnv,
  PluginWithMeta,
} from "@/lib/types";
import {
  changedManifestPluginFields,
  deriveToolName,
  pluginFieldIsOverridden,
} from "./pluginUtils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";

const SELECT_CLASS =
  "h-8 w-full rounded-lg border border-input bg-background px-3 text-sm font-mono outline-none";

const ENV_SOURCES = ["static", "oauth.access_token", "oauth.client_id"];

let envRowSeq = 0;

interface EnvRow extends ManifestSessionEnv {
  id: number;
}

function inputValue(e: React.ChangeEvent<HTMLInputElement> | React.FormEvent<HTMLInputElement>) {
  return (e.target as HTMLInputElement).value;
}

interface AddFormProps {
  existingIds: string[];
  onCreate: (params: {
    toolKey: string;
    name: string;
    displayName: string;
    version: string;
  }) => Promise<void>;
  onCancel: () => void;
}

// CliToolAddForm adds a CLI tool from one mise key. The admin searches the mise
// registry by name (or types a raw key), picks a result, and optionally pins a
// version — everything else is derived. Replaces the old nine-field form.
export function CliToolAddForm({ existingIds, onCreate, onCancel }: AddFormProps) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<CliToolRegistryItem[]>([]);
  const [searching, setSearching] = useState(false);
  const [toolKey, setToolKey] = useState("");
  const [name, setName] = useState("");
  const [version, setVersion] = useState("");
  const [creating, setCreating] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const q = query.trim();
    if (timer.current) clearTimeout(timer.current);
    if (!q) {
      setResults([]);
      setSearching(false);
      return;
    }
    setSearching(true);
    timer.current = setTimeout(async () => {
      try {
        const { data } = await searchCliToolRegistry({
          query: { q, limit: 20 },
          throwOnError: true,
        });
        setResults(data?.tools ?? []);
      } catch {
        setResults([]);
      } finally {
        setSearching(false);
      }
    }, 250);
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, [query]);

  function pick(key: string) {
    setToolKey(key);
    setName(deriveToolName(key));
    setQuery("");
    setResults([]);
  }

  const id = name.trim() ? "tool/" + name.trim() : "";
  const duplicate = !!id && existingIds.includes(id);
  const canSave = !!toolKey.trim() && !!name.trim() && !duplicate && !creating;

  async function save() {
    if (!canSave) return;
    setCreating(true);
    try {
      await onCreate({
        toolKey: toolKey.trim(),
        name: name.trim(),
        displayName: name.trim(),
        version: version.trim(),
      });
    } finally {
      setCreating(false);
    }
  }

  // A raw key (has ":" or "/") can be added directly without a registry hit.
  const directKey = query.trim();
  const looksLikeKey = /[:/]/.test(directKey);

  return (
    <DetailPanel
      onSave={save}
      onCancel={onCancel}
      saveLabel={t("common.save")}
      cancelLabel={t("common.cancel")}
      isSaving={creating}
      canSave={canSave}
    >
      <DetailPanelHeader title={t("plugins.addTool")} subtitle={t("plugins.addCliToolDesc")} />

      <div className="space-y-2">
        <Input
          nativeInput
          value={query}
          onChange={(e) => setQuery(inputValue(e))}
          type="text"
          placeholder={t("plugins.searchRegistry")}
          className="font-mono text-sm"
          size="sm"
        />
        {searching && (
          <p className="text-xs text-muted-foreground px-1">{t("plugins.searchingRegistry")}</p>
        )}
        {!searching && query.trim() && (
          <div className="rounded-lg border border-border divide-y divide-border overflow-hidden">
            {results.map((tool) => (
              <button
                key={tool.name}
                type="button"
                onClick={() => pick(tool.name ?? "")}
                className="w-full text-left px-3 py-2 hover:bg-muted flex flex-col gap-1"
              >
                <span className="font-mono text-sm">{tool.name}</span>
                <span className="flex flex-wrap gap-1">
                  {(tool.backends ?? []).map((b) => (
                    <Badge key={b} variant="outline" size="sm">
                      {b}
                    </Badge>
                  ))}
                </span>
              </button>
            ))}
            {results.length === 0 && (
              <div className="px-3 py-2 text-xs text-muted-foreground">
                {looksLikeKey ? (
                  <button
                    type="button"
                    onClick={() => pick(directKey)}
                    className="font-mono text-foreground hover:underline"
                  >
                    {t("plugins.useDirectKey", { key: directKey })}
                  </button>
                ) : (
                  t("plugins.noRegistryMatches")
                )}
              </div>
            )}
          </div>
        )}
      </div>

      {toolKey && (
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          <div className="space-y-1.5 md:col-span-2">
            <label className="text-xs font-medium text-muted-foreground">
              {t("plugins.miseKey")}
            </label>
            <Input
              nativeInput
              value={toolKey}
              onChange={(e) => setToolKey(inputValue(e))}
              type="text"
              placeholder="github:owner/repo"
              className="font-mono text-sm"
              size="sm"
            />
          </div>
          <div className="space-y-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("plugins.versionLabel")}
            </label>
            <Input
              nativeInput
              value={version}
              onChange={(e) => setVersion(inputValue(e))}
              type="text"
              placeholder="latest"
              className="font-mono text-sm"
              size="sm"
            />
          </div>
          <div className="space-y-1.5 md:col-span-3">
            <label className="text-xs font-medium text-muted-foreground">
              {t("plugins.cliToolName")}
            </label>
            <Input
              nativeInput
              value={name}
              onChange={(e) => setName(inputValue(e))}
              type="text"
              placeholder="my-cli"
              className="font-mono text-sm"
              size="sm"
            />
            <p className="text-xs text-muted-foreground">
              {id}
              {duplicate ? ` — ${t("plugins.pluginId")} ✗` : ""}
            </p>
          </div>
        </div>
      )}
    </DetailPanel>
  );
}

interface EditorProps {
  plugin: PluginWithMeta;
  oauthProviders: ManifestOAuthProvider[];
  onSave: (next: ManifestPlugin, fields: string[]) => Promise<void>;
  onResetField: (field: string) => Promise<void>;
  resettingField: string | null;
  showToast: (message: string, type?: "success" | "error") => void;
}

interface FieldOverrideActionsProps {
  overridden: boolean;
  resetting: boolean;
  disabled: boolean;
  onReset: () => void;
}

function FieldOverrideActions({
  overridden,
  resetting,
  disabled,
  onReset,
}: FieldOverrideActionsProps) {
  const { t } = useI18n();
  if (!overridden) return null;
  return (
    <div className="flex items-center gap-1">
      <Badge variant="outline" size="sm">
        {t("plugins.overriddenField")}
      </Badge>
      <Button onClick={onReset} loading={resetting} disabled={disabled} variant="ghost" size="xs">
        {t("plugins.resetField")}
      </Button>
    </div>
  );
}

function toEnvRows(envs: ManifestSessionEnv[] | undefined): EnvRow[] {
  return (envs ?? []).map((e) => ({
    id: (envRowSeq += 1),
    env_var: e.env_var ?? "",
    source: e.source || "static",
    value: e.value ?? "",
    required: !!e.required,
  }));
}

// CliToolEditor is the compact detail editor for a manifest plugin: per-binary
// version (with resolve-to-latest) and mise key, the session env mapping that
// wires the CLI's runtime variables, and an OAuth provider.
//
// A builtin's mise key is editable too: the edit becomes an override row over
// the shipped definition, which is what every other field here already does.
// The whole manifest-plugin API is admin-only, so that is who can write it.
// What builtin still forbids is removal — see pluginIsRemovable.
export function CliToolEditor({
  plugin,
  oauthProviders,
  onSave,
  onResetField,
  resettingField,
  showToast,
}: EditorProps) {
  const { t } = useI18n();
  const manifest = plugin._manifestPlugin as ManifestPlugin;
  const initialManifest = useRef(manifest);
  const binaries = manifest.binaries ?? [];

  const [versions, setVersions] = useState<Record<string, string>>(() =>
    Object.fromEntries(binaries.map((b) => [b.name, b.version ?? ""])),
  );
  const [tools, setTools] = useState<Record<string, string>>(() =>
    Object.fromEntries(binaries.map((b) => [b.name, b.tool])),
  );
  const [envRows, setEnvRows] = useState<EnvRow[]>(() => toEnvRows(manifest.session_env));
  const [oauthProvider, setOAuthProvider] = useState(manifest.oauth_provider ?? "");
  const [resolving, setResolving] = useState<Record<string, boolean>>({});
  const [saving, setSaving] = useState(false);

  async function resolveLatest(binary: ManifestBinary) {
    // Ask about the key the admin is looking at, not the saved one: after
    // repointing a tool, the old key's latest version is the wrong answer.
    const tool = (tools[binary.name] ?? binary.tool).trim();
    if (!tool) {
      showToast(t("plugins.miseKeyRequired"), "error");
      return;
    }
    setResolving((prev) => ({ ...prev, [binary.name]: true }));
    try {
      const { data } = await getCliToolLatest({
        query: { tool },
        throwOnError: true,
      });
      const v = data?.version ?? "";
      setVersions((prev) => ({ ...prev, [binary.name]: v }));
      if (v) showToast(t("plugins.latestResolved", { version: v }));
    } catch (e) {
      showToast((e as Error).message, "error");
    } finally {
      setResolving((prev) => ({ ...prev, [binary.name]: false }));
    }
  }

  function updateEnv(id: number, patch: Partial<EnvRow>) {
    setEnvRows((prev) => prev.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  }

  function reset() {
    setVersions(Object.fromEntries(binaries.map((b) => [b.name, b.version ?? ""])));
    setTools(Object.fromEntries(binaries.map((b) => [b.name, b.tool])));
    setEnvRows(toEnvRows(manifest.session_env));
    setOAuthProvider(manifest.oauth_provider ?? "");
  }

  async function resetField(field: string) {
    await onResetField(field);
  }

  async function save() {
    setSaving(true);
    try {
      const sessionEnv: ManifestSessionEnv[] = envRows
        .map((r) => {
          const e: ManifestSessionEnv = { env_var: r.env_var.trim(), source: r.source.trim() };
          if (r.value) e.value = r.value;
          if (r.required) e.required = true;
          return e;
        })
        .filter((e) => e.env_var || e.source);

      const next: ManifestPlugin = {
        ...manifest,
        binaries: binaries.map((b) => {
          const copy: ManifestBinary = { ...b };
          const v = (versions[b.name] ?? "").trim();
          if (v) copy.version = v;
          else delete copy.version;
          const tool = (tools[b.name] ?? "").trim();
          if (tool) copy.tool = tool;
          return copy;
        }),
      };
      if (sessionEnv.length) next.session_env = sessionEnv;
      else delete next.session_env;
      if (oauthProvider) next.oauth_provider = oauthProvider;
      else delete next.oauth_provider;

      const fields = changedManifestPluginFields(initialManifest.current, next);
      await onSave(next, fields);
      initialManifest.current = next;
    } finally {
      setSaving(false);
    }
  }

  const showOAuth =
    !!oauthProvider ||
    envRows.some((r) => r.source.startsWith("oauth.")) ||
    pluginFieldIsOverridden(plugin, "oauth_provider");

  return (
    <div className="border-t border-border bg-muted px-6 py-5 space-y-5">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium text-foreground">{t("plugins.configuration")}</span>
        <div className="flex items-center gap-2 shrink-0">
          <Button onClick={reset} variant="ghost" size="xs">
            {t("common.reset")}
          </Button>
          <Button
            onClick={() => void save()}
            loading={saving}
            disabled={resettingField !== null}
            variant="default"
            size="xs"
          >
            {t("common.save")}
          </Button>
        </div>
      </div>

      {/* Binaries — version only */}
      <div className="space-y-2">
        <div className="flex items-center justify-between gap-2">
          <p className="text-xs font-semibold text-muted-foreground">{t("plugins.binaries")}</p>
          <FieldOverrideActions
            overridden={pluginFieldIsOverridden(plugin, "binaries")}
            resetting={resettingField === "binaries"}
            disabled={resettingField !== null}
            onReset={() => void resetField("binaries")}
          />
        </div>
        {binaries.map((binary) => (
          <div key={binary.name} className="rounded-lg border border-border bg-background p-4">
            <div className="grid grid-cols-1 md:grid-cols-[1fr_auto] gap-3 items-end">
              <div className="space-y-1.5 min-w-0">
                <span className="text-xs font-semibold text-foreground">{binary.name}</span>
                <Input
                  nativeInput
                  value={tools[binary.name] ?? ""}
                  onChange={(e) => setTools((prev) => ({ ...prev, [binary.name]: inputValue(e) }))}
                  type="text"
                  placeholder="github:owner/repo"
                  className="font-mono text-sm"
                  size="sm"
                />
              </div>
              <div className="flex items-end gap-2">
                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">
                    {t("plugins.versionLabel")}
                  </label>
                  <Input
                    nativeInput
                    value={versions[binary.name] ?? ""}
                    onChange={(e) =>
                      setVersions((prev) => ({ ...prev, [binary.name]: inputValue(e) }))
                    }
                    type="text"
                    placeholder="latest"
                    className="font-mono text-sm w-36"
                    size="sm"
                  />
                </div>
                <Button
                  onClick={() => void resolveLatest(binary)}
                  loading={!!resolving[binary.name]}
                  variant="outline"
                  size="sm"
                >
                  {t("plugins.updateToLatest")}
                </Button>
              </div>
            </div>
          </div>
        ))}
      </div>

      {/* Session environment — which env vars the CLI receives */}
      <div className="space-y-2">
        <div className="flex items-center justify-between gap-2">
          <p className="text-xs font-semibold text-muted-foreground">{t("plugins.sessionEnv")}</p>
          <div className="flex items-center gap-1">
            <FieldOverrideActions
              overridden={pluginFieldIsOverridden(plugin, "session_env")}
              resetting={resettingField === "session_env"}
              disabled={resettingField !== null}
              onReset={() => void resetField("session_env")}
            />
            <Button
              onClick={() =>
                setEnvRows((prev) => [
                  ...prev,
                  {
                    id: (envRowSeq += 1),
                    env_var: "",
                    source: "static",
                    value: "",
                    required: false,
                  },
                ])
              }
              variant="ghost"
              size="xs"
            >
              {t("plugins.addVariable")}
            </Button>
          </div>
        </div>
        {envRows.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("plugins.noSessionEnv")}</p>
        ) : (
          envRows.map((row) => (
            <div
              key={row.id}
              className="rounded-lg border border-border bg-background p-3 space-y-3"
            >
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
                <Input
                  nativeInput
                  value={row.env_var}
                  onChange={(e) => updateEnv(row.id, { env_var: inputValue(e) })}
                  type="text"
                  placeholder="MY_ENV_VAR"
                  className="font-mono text-sm"
                  size="sm"
                />
                <select
                  value={row.source}
                  onChange={(e) => updateEnv(row.id, { source: e.target.value })}
                  className={SELECT_CLASS}
                >
                  {ENV_SOURCES.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </select>
                <Input
                  nativeInput
                  value={row.value ?? ""}
                  onChange={(e) => updateEnv(row.id, { value: inputValue(e) })}
                  type="text"
                  placeholder={t("plugins.valueStaticOnly")}
                  className="font-mono text-sm"
                  size="sm"
                  disabled={row.source !== "static"}
                />
              </div>
              <div className="flex items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <Switch
                    checked={!!row.required}
                    onCheckedChange={(checked) => updateEnv(row.id, { required: checked })}
                  />
                  <span className="text-xs text-muted-foreground">{t("common.required")}</span>
                </div>
                <Button
                  onClick={() => setEnvRows((prev) => prev.filter((r) => r.id !== row.id))}
                  variant="ghost"
                  size="xs"
                  className="text-destructive-foreground hover:text-destructive-foreground"
                >
                  {t("common.remove")}
                </Button>
              </div>
            </div>
          ))
        )}
      </div>

      {/* OAuth provider — only when an env var sources a token */}
      {showOAuth && (
        <div className="space-y-2">
          <div className="flex items-center justify-between gap-2">
            <p className="text-xs font-semibold text-muted-foreground">
              {t("plugins.oauthProvider")}
            </p>
            <FieldOverrideActions
              overridden={pluginFieldIsOverridden(plugin, "oauth_provider")}
              resetting={resettingField === "oauth_provider"}
              disabled={resettingField !== null}
              onReset={() => void resetField("oauth_provider")}
            />
          </div>
          <select
            value={oauthProvider}
            onChange={(e) => setOAuthProvider(e.target.value)}
            className={`${SELECT_CLASS} max-w-xs`}
          >
            <option value="">— none —</option>
            {oauthProviders.map((p) => (
              <option key={p.id} value={p.id}>
                {p.id}
              </option>
            ))}
          </select>
        </div>
      )}
    </div>
  );
}
