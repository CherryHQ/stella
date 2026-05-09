import { nextRowID } from "./pluginUtils";
import type { ManifestInstallDraft } from "./pluginUtils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";

interface Props {
  draft: ManifestInstallDraft;
  onChange: (draft: ManifestInstallDraft) => void;
  onSave: () => void;
  onReset: () => void;
}

export function ManifestInstallEditor({ draft, onChange, onSave, onReset }: Props) {
  const { t } = useI18n();
  function update(partial: Partial<ManifestInstallDraft>) {
    onChange({ ...draft, ...partial });
  }

  function addBinary() {
    update({
      binaries: [
        ...draft.binaries,
        { id: nextRowID(), name: "", repo: "", version: "", bin_path: "", exe: "" },
      ],
    });
  }

  function removeBinary(index: number) {
    const next = [...draft.binaries];
    next.splice(index, 1);
    update({ binaries: next });
  }

  function updateBinary(index: number, field: string, value: string) {
    const next = [...draft.binaries];
    next[index] = { ...next[index], [field]: value };
    update({ binaries: next });
  }

  function addSessionEnv() {
    update({
      session_env: [
        ...draft.session_env,
        { id: nextRowID(), env_var: "", source: "static", value: "", required: false },
      ],
    });
  }

  function removeSessionEnv(index: number) {
    const next = [...draft.session_env];
    next.splice(index, 1);
    update({ session_env: next });
  }

  function updateSessionEnv(index: number, field: string, value: string | boolean) {
    const next = [...draft.session_env];
    next[index] = { ...next[index], [field]: value };
    update({ session_env: next });
  }

  return (
    <div className="px-4 pb-4 border-t border-border bg-muted/30">
      <div className="pt-4 space-y-4">
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div>
            <p className="text-sm font-medium">Tool definition</p>
            <p className="text-xs text-muted-foreground mt-1">
              Saved to <code className="font-mono">$STELLA_HOME/plugins.yaml</code>; binaries sync
              after save.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button onClick={onReset} variant="ghost" size="xs">
              Reset
            </Button>
            <Button onClick={onSave} variant="default" size="xs">
              Save definition
            </Button>
          </div>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4">
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">ID</label>
            <Input
              nativeInput
              value={draft.id}
              onChange={(e) => update({ id: (e.target as HTMLInputElement).value })}
              className="font-mono"
              size="sm"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Kind</label>
            <Input
              nativeInput
              value={draft.kind}
              onChange={(e) => update({ kind: (e.target as HTMLInputElement).value })}
              className="font-mono"
              size="sm"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Name</label>
            <Input
              nativeInput
              value={draft.name}
              onChange={(e) => update({ name: (e.target as HTMLInputElement).value })}
              className="font-mono"
              size="sm"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Display name</label>
            <Input
              nativeInput
              value={draft.display_name}
              onChange={(e) => update({ display_name: (e.target as HTMLInputElement).value })}
              size="sm"
            />
          </div>
          <div className="space-y-1 md:col-span-2 xl:col-span-4">
            <label className="text-xs font-medium text-muted-foreground">Description</label>
            <Input
              nativeInput
              value={draft.description}
              onChange={(e) => update({ description: (e.target as HTMLInputElement).value })}
              size="sm"
            />
          </div>

          {/* Binaries */}
          <div className="space-y-1 md:col-span-2 xl:col-span-4">
            <div className="flex items-center justify-between gap-2">
              <label className="text-xs font-medium text-muted-foreground">Binaries</label>
              <Button onClick={addBinary} variant="ghost" size="xs">
                Add binary
              </Button>
            </div>
            <div className="space-y-2">
              {draft.binaries.map((binary, index) => (
                <div
                  key={binary.id}
                  className="grid grid-cols-1 md:grid-cols-[1fr_1.4fr_0.8fr_0.8fr_0.8fr_auto] gap-2 rounded-lg border border-border p-2"
                >
                  <Input
                    nativeInput
                    value={binary.name}
                    onChange={(e) =>
                      updateBinary(index, "name", (e.target as HTMLInputElement).value)
                    }
                    placeholder="binary name"
                    className="font-mono"
                    size="sm"
                  />
                  <Input
                    nativeInput
                    value={binary.repo}
                    onChange={(e) =>
                      updateBinary(index, "repo", (e.target as HTMLInputElement).value)
                    }
                    placeholder="owner/repo"
                    className="font-mono"
                    size="sm"
                  />
                  <Input
                    nativeInput
                    value={binary.version ?? ""}
                    onChange={(e) =>
                      updateBinary(index, "version", (e.target as HTMLInputElement).value)
                    }
                    placeholder="version"
                    className="font-mono"
                    size="sm"
                  />
                  <Input
                    nativeInput
                    value={binary.bin_path ?? ""}
                    onChange={(e) =>
                      updateBinary(index, "bin_path", (e.target as HTMLInputElement).value)
                    }
                    placeholder="bin path"
                    className="font-mono"
                    size="sm"
                  />
                  <Input
                    nativeInput
                    value={binary.exe ?? ""}
                    onChange={(e) =>
                      updateBinary(index, "exe", (e.target as HTMLInputElement).value)
                    }
                    placeholder="exe"
                    className="font-mono"
                    size="sm"
                  />
                  <Button
                    onClick={() => removeBinary(index)}
                    variant="ghost"
                    size="xs"
                    className="text-destructive"
                  >
                    {t("common.remove")}
                  </Button>
                </div>
              ))}
              {draft.binaries.length === 0 && (
                <div className="rounded-lg border border-dashed border-border px-3 py-3 text-xs text-muted-foreground">
                  No binaries declared.
                </div>
              )}
            </div>
          </div>

          {/* Session env */}
          <div className="space-y-1 md:col-span-2 xl:col-span-4">
            <div className="flex items-center justify-between gap-2">
              <label className="text-xs font-medium text-muted-foreground">
                Session environment
              </label>
              <Button onClick={addSessionEnv} variant="ghost" size="xs">
                Add env
              </Button>
            </div>
            <div className="space-y-2">
              {draft.session_env.map((env, index) => (
                <div
                  key={env.id}
                  className="grid grid-cols-1 md:grid-cols-[1fr_1fr_1fr_auto_auto] gap-2 rounded-lg border border-border p-2 items-center"
                >
                  <Input
                    nativeInput
                    value={env.env_var}
                    onChange={(e) =>
                      updateSessionEnv(index, "env_var", (e.target as HTMLInputElement).value)
                    }
                    placeholder="ENV_VAR"
                    className="font-mono"
                    size="sm"
                  />
                  <select
                    value={env.source}
                    onChange={(e) => updateSessionEnv(index, "source", e.target.value)}
                    className="select select-bordered select-sm w-full font-mono"
                  >
                    <option value="static">static</option>
                    <option value="oauth.access_token">oauth.access_token</option>
                    <option value="oauth.client_id">oauth.client_id</option>
                    <option value="oauth.brand">oauth.brand</option>
                  </select>
                  <Input
                    nativeInput
                    value={env.value ?? ""}
                    onChange={(e) =>
                      updateSessionEnv(index, "value", (e.target as HTMLInputElement).value)
                    }
                    placeholder="value (static only)"
                    className="font-mono"
                    size="sm"
                  />
                  <div className="flex items-center gap-2">
                    <Switch
                      checked={!!env.required}
                      onCheckedChange={(checked) => updateSessionEnv(index, "required", checked)}
                    />
                    <span className="text-xs text-muted-foreground">required</span>
                  </div>
                  <Button
                    onClick={() => removeSessionEnv(index)}
                    variant="ghost"
                    size="xs"
                    className="text-destructive"
                  >
                    {t("common.remove")}
                  </Button>
                </div>
              ))}
              {draft.session_env.length === 0 && (
                <div className="rounded-lg border border-dashed border-border px-3 py-3 text-xs text-muted-foreground">
                  No session environment variables declared.
                </div>
              )}
            </div>
          </div>

          {/* OAuth */}
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">OAuth provider</label>
            <Input
              nativeInput
              value={draft.oauth_provider}
              onChange={(e) => update({ oauth_provider: (e.target as HTMLInputElement).value })}
              placeholder="github"
              className="font-mono"
              size="sm"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">
              Provider config field
            </label>
            <Input
              nativeInput
              value={draft.oauth_provider_config_field}
              onChange={(e) =>
                update({ oauth_provider_config_field: (e.target as HTMLInputElement).value })
              }
              placeholder="brand"
              className="font-mono"
              size="sm"
            />
          </div>
          <div className="space-y-1 md:col-span-2">
            <label className="text-xs font-medium text-muted-foreground">Provider choices</label>
            <Input
              nativeInput
              value={draft.oauth_provider_choices}
              onChange={(e) =>
                update({ oauth_provider_choices: (e.target as HTMLInputElement).value })
              }
              placeholder="feishu, lark"
              className="font-mono"
              size="sm"
            />
          </div>
        </div>
      </div>
    </div>
  );
}
