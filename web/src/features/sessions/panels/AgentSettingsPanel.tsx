import { useCallback, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { meQueryOptions } from "@/lib/queries/me";
import { api } from "@/lib/api";
import type { AgentDetail, AgentSandbox } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

interface Props {
  agentId: string;
}

type Tab = "config" | "prompt" | "advanced";

interface Form {
  name: string;
  model: string;
  model_strong: string;
  model_fast: string;
  system_prompt: string;
  soul: string;
  scope: "system" | "restricted";
  enabled: boolean;
  sandbox: AgentSandbox;
}

function emptyForm(): Form {
  return {
    name: "",
    model: "",
    model_strong: "",
    model_fast: "",
    system_prompt: "",
    soul: "",
    scope: "restricted",
    enabled: true,
    sandbox: { network: { mode: "disabled", allowlist: [] } },
  };
}

function normalizeSandbox(s: unknown): AgentSandbox {
  const sb = s as AgentSandbox | undefined;
  return {
    network: {
      mode: sb?.network?.mode ?? "disabled",
      allowlist: Array.isArray(sb?.network?.allowlist) ? sb!.network.allowlist : [],
    },
  };
}

function ModelCombo({
  label,
  value,
  placeholder,
  optional,
  models,
  onChange,
}: {
  label: string;
  value: string;
  placeholder: string;
  optional?: boolean;
  models: string[];
  onChange: (v: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const filtered = search
    ? models.filter((m) => m.toLowerCase().includes(search.toLowerCase()))
    : models;

  return (
    <div className="relative">
      <div className="flex items-center justify-between mb-1">
        <label className="text-sm font-mono">{label}</label>
        {optional && <span className="text-xs text-muted-foreground">(optional)</span>}
      </div>
      <Input
        nativeInput
        type="text"
        value={value}
        placeholder={placeholder}
        className="text-sm font-mono"
        autoComplete="off"
        onChange={(e) => {
          onChange((e.target as HTMLInputElement).value);
          setSearch((e.target as HTMLInputElement).value);
          setOpen(models.length > 0);
        }}
        onFocus={() => {
          setSearch("");
          setOpen(models.length > 0);
        }}
        onBlur={() => setTimeout(() => setOpen(false), 150)}
      />
      {open && filtered.length > 0 && (
        <div className="absolute z-20 mt-1 w-full max-h-48 overflow-y-auto bg-popover border border-border rounded-xl shadow-lg py-1">
          {filtered.map((m) => (
            <button
              key={m}
              type="button"
              onMouseDown={() => {
                onChange(m);
                setOpen(false);
              }}
              className={cn(
                "w-full text-left px-3 py-1.5 text-xs font-mono hover:bg-muted cursor-pointer",
                value === m ? "text-primary" : "text-muted-foreground",
              )}
            >
              {m}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export function AgentSettingsPanel({ agentId }: Props) {
  const { data: me } = useQuery(meQueryOptions);
  const isAdmin = me?.is_admin ?? false;

  const [tab, setTab] = useState<Tab>("config");
  const [form, setForm] = useState<Form>(emptyForm());
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [models, setModels] = useState<string[]>([]);

  const patchForm = (patch: Partial<Form>) => {
    setForm((f) => ({ ...f, ...patch }));
    setDirty(true);
  };

  const load = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const [agent, modelList] = await Promise.all([
        api<AgentDetail>("GET", `/api/agents/${encodeURIComponent(agentId)}`),
        api<{ provider: string; model: string }[]>("GET", "/api/models").catch(() => []),
      ]);
      setForm({
        name: agent.name,
        model: agent.model,
        model_strong: agent.model_strong ?? "",
        model_fast: agent.model_fast ?? "",
        system_prompt: agent.system_prompt ?? "",
        soul: agent.soul ?? "",
        scope: agent.scope ?? "restricted",
        enabled: agent.enabled,
        sandbox: normalizeSandbox(agent.sandbox),
      });
      setModels((modelList ?? []).map((m) => `${m.provider}/${m.model}`));
      setDirty(false);
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    void load();
  }, [load]);

  const save = useCallback(async () => {
    setSaving(true);
    try {
      await api("PUT", `/api/agents/${encodeURIComponent(agentId)}`, {
        name: form.name,
        model: form.model,
        model_strong: form.model_strong,
        model_fast: form.model_fast,
        system_prompt: form.system_prompt,
        soul: form.soul,
        scope: form.scope,
        enabled: form.enabled,
        sandbox: form.sandbox,
      });
      setDirty(false);
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  }, [agentId, form]);

  const tabs: { id: Tab; label: string }[] = [
    { id: "config", label: "Config" },
    { id: "prompt", label: "Prompt" },
    { id: "advanced", label: "Advanced" },
  ];

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
        Loading…
      </div>
    );
  }

  return (
    <div className="flex flex-col flex-1 overflow-hidden">
      {/* Tabs */}
      <div className="flex border-b border-border px-4 flex-shrink-0">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            className={cn(
              "px-4 py-3 text-sm border-b-2 -mb-px transition-colors",
              tab === t.id
                ? "text-foreground font-medium border-primary"
                : "text-muted-foreground border-transparent hover:text-foreground",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div className="flex-1 overflow-y-auto p-6 space-y-5">
        {tab === "config" && (
          <>
            <div>
              <label className="block text-sm font-mono mb-1">Name</label>
              <Input
                nativeInput
                value={form.name}
                onChange={(e) => patchForm({ name: (e.target as HTMLInputElement).value })}
                className="text-sm"
              />
            </div>
            <div>
              <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-3">
                Models{" "}
                <span className="normal-case tracking-normal text-muted-foreground/50">
                  — provider/model
                </span>
              </p>
              <div className="space-y-3">
                <ModelCombo
                  label="Default"
                  value={form.model}
                  placeholder="anthropic/claude-..."
                  models={models}
                  onChange={(v) => patchForm({ model: v })}
                />
                <div className="grid grid-cols-2 gap-3">
                  <ModelCombo
                    label="Strong"
                    value={form.model_strong}
                    placeholder="Falls back to default"
                    optional
                    models={models}
                    onChange={(v) => patchForm({ model_strong: v })}
                  />
                  <ModelCombo
                    label="Fast"
                    value={form.model_fast}
                    placeholder="Falls back to default"
                    optional
                    models={models}
                    onChange={(v) => patchForm({ model_fast: v })}
                  />
                </div>
              </div>
            </div>
            {isAdmin && (
              <div>
                <label className="block text-sm font-mono mb-1">Scope</label>
                <select
                  value={form.scope}
                  onChange={(e) => patchForm({ scope: e.target.value as "system" | "restricted" })}
                  className="w-full rounded-lg border border-input bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
                >
                  <option value="system">system — all users can access</option>
                  <option value="restricted">restricted — only assigned users</option>
                </select>
              </div>
            )}
            <div className="pt-2 border-t border-border">
              <label className="flex items-center gap-3 cursor-pointer">
                <Switch
                  checked={form.enabled}
                  onCheckedChange={(checked) => patchForm({ enabled: checked })}
                />
                <span className="text-sm">Enabled</span>
              </label>
            </div>
          </>
        )}

        {tab === "prompt" && (
          <>
            <div>
              <label className="block text-sm font-mono mb-1">Soul</label>
              <p className="text-xs text-muted-foreground mb-1">
                Default personality for all users.
              </p>
              <Textarea
                value={form.soul}
                onChange={(e) => patchForm({ soul: (e.target as HTMLTextAreaElement).value })}
                rows={4}
                placeholder="Personality and behavior tone…"
                className="text-sm font-mono"
              />
            </div>
            <div>
              <label className="block text-sm font-mono mb-1">System Prompt</label>
              <Textarea
                value={form.system_prompt}
                onChange={(e) =>
                  patchForm({ system_prompt: (e.target as HTMLTextAreaElement).value })
                }
                rows={12}
                className="text-sm font-mono"
              />
            </div>
          </>
        )}

        {tab === "advanced" && (
          <div className="rounded-xl border border-border p-4 bg-muted/40 space-y-4">
            <div>
              <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-1">
                Network Policy
              </p>
              <p className="text-xs text-muted-foreground">
                Sandbox backend is configured on the{" "}
                <a href="/settings/plugins" className="text-primary underline underline-offset-4">
                  Plugins
                </a>{" "}
                page.
              </p>
            </div>
            <div>
              <label className="block text-sm font-mono mb-1">Network Mode</label>
              <select
                value={form.sandbox.network.mode}
                onChange={(e) =>
                  patchForm({
                    sandbox: normalizeSandbox({
                      network: {
                        mode: e.target.value,
                        allowlist: form.sandbox.network.allowlist,
                      },
                    }),
                  })
                }
                className="w-full rounded-lg border border-input bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring"
              >
                <option value="disabled">disabled — block outbound network</option>
                <option value="allow_all">allow_all — allow outbound network</option>
                <option value="whitelist">whitelist — only listed hosts/CIDRs</option>
              </select>
            </div>
            {form.sandbox.network.mode === "whitelist" && (
              <div>
                <label className="block text-sm font-mono mb-1">Allowlist</label>
                <Textarea
                  value={form.sandbox.network.allowlist.join("\n")}
                  onChange={(e) =>
                    patchForm({
                      sandbox: normalizeSandbox({
                        network: {
                          mode: "whitelist",
                          allowlist: (e.target as HTMLTextAreaElement).value
                            .split(/\r?\n|,/)
                            .map((v) => v.trim())
                            .filter(Boolean),
                        },
                      }),
                    })
                  }
                  placeholder={"api.github.com\npypi.org\n10.0.0.0/8"}
                  rows={4}
                  className="text-sm font-mono"
                />
              </div>
            )}
          </div>
        )}
      </div>

      {/* Footer */}
      <div className="flex items-center gap-2 px-6 py-4 border-t border-border flex-shrink-0">
        <Button onClick={() => void save()} disabled={saving || !dirty} size="sm">
          {saving ? "Saving…" : "Save"}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => void load()} disabled={saving}>
          Reset
        </Button>
      </div>
    </div>
  );
}
