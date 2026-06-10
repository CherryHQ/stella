import { useEffect, useRef, useState } from "react";
import { getCliToolLatest, searchCliToolRegistry } from "@/lib/api-client/sdk.gen";
import type { CliToolRegistryItem } from "@/lib/api-client/types.gen";
import type { ManifestBinary, ManifestPlugin, PluginWithMeta } from "@/lib/types";
import { deriveToolName } from "./pluginUtils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { useI18n } from "@/lib/i18n";
import { DetailPanel, DetailPanelHeader } from "@/features/settings/SettingsDetailPanel";

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
  onSave: (next: ManifestPlugin) => Promise<void>;
  showToast: (message: string, type?: "success" | "error") => void;
}

// CliToolEditor is the compact detail editor for a plain CLI tool: per-binary
// version, with a one-click resolve-to-latest. The mise key is shown read-only —
// changing it means redefining the tool, which is an add/remove, not an edit.
export function CliToolEditor({ plugin, onSave, showToast }: EditorProps) {
  const { t } = useI18n();
  const manifest = plugin._manifestPlugin as ManifestPlugin;
  const binaries = manifest.binaries ?? [];

  const [versions, setVersions] = useState<Record<string, string>>(() =>
    Object.fromEntries(binaries.map((b) => [b.name, b.version ?? ""])),
  );
  const [resolving, setResolving] = useState<Record<string, boolean>>({});
  const [saving, setSaving] = useState(false);

  async function resolveLatest(binary: ManifestBinary) {
    setResolving((prev) => ({ ...prev, [binary.name]: true }));
    try {
      const { data } = await getCliToolLatest({
        query: { tool: binary.tool },
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

  function reset() {
    setVersions(Object.fromEntries(binaries.map((b) => [b.name, b.version ?? ""])));
  }

  async function save() {
    setSaving(true);
    try {
      const next: ManifestPlugin = {
        ...manifest,
        binaries: binaries.map((b) => {
          const copy: ManifestBinary = { ...b };
          const v = (versions[b.name] ?? "").trim();
          if (v) copy.version = v;
          else delete copy.version;
          return copy;
        }),
      };
      await onSave(next);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="border-t border-border bg-muted px-6 py-5 space-y-4">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium text-foreground">{t("plugins.binaries")}</span>
        <div className="flex items-center gap-2 shrink-0">
          <Button onClick={reset} variant="ghost" size="xs">
            {t("common.reset")}
          </Button>
          <Button onClick={() => void save()} loading={saving} variant="default" size="xs">
            {t("common.save")}
          </Button>
        </div>
      </div>
      <div className="space-y-3">
        {binaries.map((binary) => (
          <div key={binary.name} className="rounded-lg border border-border bg-background p-4">
            <div className="grid grid-cols-1 md:grid-cols-[1fr_auto] gap-3 items-end">
              <div className="space-y-1.5 min-w-0">
                <span className="text-xs font-semibold text-foreground">{binary.name}</span>
                <p className="font-mono text-xs text-muted-foreground truncate">{binary.tool}</p>
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
    </div>
  );
}
