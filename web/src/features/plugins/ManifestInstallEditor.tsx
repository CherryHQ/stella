import { nextRowID } from "./pluginUtils";
import type { ManifestInstallDraft } from "./pluginUtils";
import type { ManifestOAuthProvider } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";

const SELECT_CLASS =
  "h-9 w-full rounded-lg border border-input bg-background px-3 text-sm font-mono outline-none sm:h-8";

interface Props {
  draft: ManifestInstallDraft;
  oauthProviders: ManifestOAuthProvider[];
  onChange: (draft: ManifestInstallDraft) => void;
  onSave: () => void;
  onReset: () => void;
}

function SectionHeader({ title, action }: { title: string; action?: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-2 pb-2 border-b border-border">
      <span className="text-sm font-medium text-foreground">{title}</span>
      {action}
    </div>
  );
}

function FieldLabel({ children }: { children: React.ReactNode }) {
  return <label className="block text-xs font-medium text-muted-foreground mb-1">{children}</label>;
}

export function ManifestInstallEditor({ draft, oauthProviders, onChange, onSave, onReset }: Props) {
  const { t } = useI18n();
  function update(partial: Partial<ManifestInstallDraft>) {
    onChange({ ...draft, ...partial });
  }

  function addBinary() {
    update({
      binaries: [
        ...draft.binaries,
        { id: nextRowID(), name: "", tool: "", version: "", bin_path: "", bin: "" },
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
    <div className="border-t border-border bg-muted px-6 py-5 space-y-6">
      {/* Header */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm font-semibold">Edit definition</p>
          <p className="text-xs text-muted-foreground mt-0.5">
            Override the manifest definition. Binaries sync on save.
          </p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <Button onClick={onReset} variant="ghost" size="xs">
            Reset
          </Button>
          <Button onClick={onSave} variant="default" size="xs">
            Save
          </Button>
        </div>
      </div>

      {/* Identity */}
      <div className="space-y-3">
        <SectionHeader title="Identity" />
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <div>
            <FieldLabel>ID</FieldLabel>
            <Input
              nativeInput
              value={draft.id}
              onChange={(e) => update({ id: (e.target as HTMLInputElement).value })}
              className="font-mono"
              size="sm"
            />
          </div>
          <div>
            <FieldLabel>Kind</FieldLabel>
            <Input
              nativeInput
              value={draft.kind}
              onChange={(e) => update({ kind: (e.target as HTMLInputElement).value })}
              className="font-mono"
              size="sm"
            />
          </div>
          <div>
            <FieldLabel>Name</FieldLabel>
            <Input
              nativeInput
              value={draft.name}
              onChange={(e) => update({ name: (e.target as HTMLInputElement).value })}
              className="font-mono"
              size="sm"
            />
          </div>
          <div>
            <FieldLabel>Display name</FieldLabel>
            <Input
              nativeInput
              value={draft.display_name}
              onChange={(e) => update({ display_name: (e.target as HTMLInputElement).value })}
              size="sm"
            />
          </div>
          <div className="col-span-2 lg:col-span-4">
            <FieldLabel>Description</FieldLabel>
            <Input
              nativeInput
              value={draft.description}
              onChange={(e) => update({ description: (e.target as HTMLInputElement).value })}
              size="sm"
            />
          </div>
        </div>
      </div>

      {/* Binaries */}
      <div className="space-y-3">
        <SectionHeader
          title="Binaries"
          action={
            <Button onClick={addBinary} variant="ghost" size="xs">
              + Add binary
            </Button>
          }
        />
        {draft.binaries.length === 0 ? (
          <div className="rounded-lg border border-dashed border-border bg-background/60 px-4 py-5 text-center text-xs text-muted-foreground">
            No binaries declared. Add one to install a CLI from a GitHub release.
          </div>
        ) : (
          <div className="space-y-3">
            {draft.binaries.map((binary, index) => (
              <div
                key={binary.id}
                className="rounded-lg border border-border bg-background p-4 space-y-3"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-semibold text-muted-foreground">
                    Binary {index + 1}
                    {binary.name && (
                      <span className="ml-2 font-mono font-normal normal-case text-foreground">
                        {binary.name}
                      </span>
                    )}
                  </span>
                  <Button
                    onClick={() => removeBinary(index)}
                    variant="ghost"
                    size="xs"
                    className="text-destructive hover:text-destructive"
                  >
                    {t("common.remove")}
                  </Button>
                </div>
                <div className="grid grid-cols-2 gap-3 lg:grid-cols-3">
                  <div>
                    <FieldLabel>Name</FieldLabel>
                    <Input
                      nativeInput
                      value={binary.name}
                      onChange={(e) =>
                        updateBinary(index, "name", (e.target as HTMLInputElement).value)
                      }
                      placeholder="my-cli"
                      className="font-mono"
                      size="sm"
                    />
                  </div>
                  <div className="col-span-1 lg:col-span-2">
                    <FieldLabel>GitHub repo (owner/repo)</FieldLabel>
                    <Input
                      nativeInput
                      value={binary.tool}
                      onChange={(e) =>
                        updateBinary(index, "tool", (e.target as HTMLInputElement).value)
                      }
                      placeholder="owner/repo"
                      className="font-mono"
                      size="sm"
                    />
                  </div>
                  <div>
                    <FieldLabel>Version</FieldLabel>
                    <Input
                      nativeInput
                      value={binary.version ?? ""}
                      onChange={(e) =>
                        updateBinary(index, "version", (e.target as HTMLInputElement).value)
                      }
                      placeholder="latest"
                      className="font-mono"
                      size="sm"
                    />
                  </div>
                  <div>
                    <FieldLabel>Bin path</FieldLabel>
                    <Input
                      nativeInput
                      value={binary.bin_path ?? ""}
                      onChange={(e) =>
                        updateBinary(index, "bin_path", (e.target as HTMLInputElement).value)
                      }
                      placeholder="bin/in/archive"
                      className="font-mono"
                      size="sm"
                    />
                  </div>
                  <div>
                    <FieldLabel>Executable</FieldLabel>
                    <Input
                      nativeInput
                      value={binary.bin ?? ""}
                      onChange={(e) =>
                        updateBinary(index, "bin", (e.target as HTMLInputElement).value)
                      }
                      placeholder="exe name"
                      className="font-mono"
                      size="sm"
                    />
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Session environment */}
      <div className="space-y-3">
        <SectionHeader
          title="Session environment"
          action={
            <Button onClick={addSessionEnv} variant="ghost" size="xs">
              + Add variable
            </Button>
          }
        />
        {draft.session_env.length === 0 ? (
          <div className="rounded-lg border border-dashed border-border bg-background/60 px-4 py-5 text-center text-xs text-muted-foreground">
            No session environment variables declared.
          </div>
        ) : (
          <div className="space-y-3">
            {draft.session_env.map((env, index) => (
              <div
                key={env.id}
                className="rounded-lg border border-border bg-background p-4 space-y-3"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-semibold text-muted-foreground">
                    Variable {index + 1}
                    {env.env_var && (
                      <span className="ml-2 font-mono font-normal normal-case text-foreground">
                        {env.env_var}
                      </span>
                    )}
                  </span>
                  <Button
                    onClick={() => removeSessionEnv(index)}
                    variant="ghost"
                    size="xs"
                    className="text-destructive hover:text-destructive"
                  >
                    {t("common.remove")}
                  </Button>
                </div>
                <div className="grid grid-cols-2 gap-3 lg:grid-cols-3">
                  <div>
                    <FieldLabel>Variable name</FieldLabel>
                    <Input
                      nativeInput
                      value={env.env_var}
                      onChange={(e) =>
                        updateSessionEnv(index, "env_var", (e.target as HTMLInputElement).value)
                      }
                      placeholder="MY_ENV_VAR"
                      className="font-mono"
                      size="sm"
                    />
                  </div>
                  <div>
                    <FieldLabel>Source</FieldLabel>
                    <select
                      value={env.source}
                      onChange={(e) => updateSessionEnv(index, "source", e.target.value)}
                      className={SELECT_CLASS}
                    >
                      <option value="static">static</option>
                      <option value="oauth.access_token">oauth.access_token</option>
                      <option value="oauth.client_id">oauth.client_id</option>
                      <option value="oauth.brand">oauth.brand</option>
                    </select>
                  </div>
                  <div>
                    <FieldLabel>Value (static only)</FieldLabel>
                    <Input
                      nativeInput
                      value={env.value ?? ""}
                      onChange={(e) =>
                        updateSessionEnv(index, "value", (e.target as HTMLInputElement).value)
                      }
                      placeholder="value"
                      className="font-mono"
                      size="sm"
                      disabled={env.source !== "static"}
                    />
                  </div>
                </div>
                <div className="flex items-center gap-2.5 pt-1">
                  <Switch
                    checked={!!env.required}
                    onCheckedChange={(checked) => updateSessionEnv(index, "required", checked)}
                  />
                  <span className="text-xs text-muted-foreground">Required</span>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* OAuth provider — only shown when relevant */}
      {(draft.oauth_provider || draft.session_env.some((e) => e.source.startsWith("oauth."))) && (
        <div className="space-y-3">
          <SectionHeader title="OAuth provider" />
          <div className="max-w-xs">
            <select
              value={draft.oauth_provider}
              onChange={(e) => update({ oauth_provider: e.target.value })}
              className={SELECT_CLASS}
            >
              <option value="">— none —</option>
              {oauthProviders.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.id}
                </option>
              ))}
            </select>
          </div>
        </div>
      )}
    </div>
  );
}
