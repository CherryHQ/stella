import { normalizeSandbox } from "../AgentsPage";
import type { AgentsPageState } from "../AgentsPage";
import { Textarea } from "@/components/ui/textarea";

interface Props {
  state: AgentsPageState;
  onSetState: (patch: Partial<AgentsPageState>) => void;
}

export function AdvancedTab({ state, onSetState }: Props) {
  const { form } = state;

  const setForm = (patch: Partial<typeof form>) =>
    onSetState({ form: { ...form, ...patch } });

  const allowlistText = (form.sandbox?.network?.allowlist ?? []).join("\n");

  const updateSandboxAllowlist = (value: string) => {
    setForm({
      sandbox: normalizeSandbox({
        network: {
          mode: form.sandbox?.network?.mode ?? "disabled",
          allowlist: value.split(/\r?\n|,/).map((v) => v.trim()).filter(Boolean),
        },
      }),
    });
  };

  const networkMode = form.sandbox?.network?.mode ?? "disabled";

  return (
    <div className="rounded-xl border border-border p-4 bg-muted/40 space-y-4">
      <div>
        <p className="text-xs font-mono font-medium text-muted-foreground uppercase tracking-wider mb-1">
          Network Policy
        </p>
        <p className="text-xs text-muted-foreground">
          Sandbox backend is configured on the{" "}
          <a href="/plugins" className="text-primary underline underline-offset-4">Plugins</a> page.
        </p>
      </div>
      <div>
        <label className="block text-sm font-mono mb-1">Network Mode</label>
        <select
          value={networkMode}
          onChange={(e) =>
            setForm({
              sandbox: normalizeSandbox({
                network: {
                  mode: e.target.value as "disabled" | "allow_all" | "whitelist",
                  allowlist: form.sandbox?.network?.allowlist ?? [],
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
      {networkMode === "whitelist" && (
        <div>
          <label className="block text-sm font-mono mb-1">Allowlist</label>
          <Textarea
            value={allowlistText}
            onChange={(e) => updateSandboxAllowlist((e.target as HTMLTextAreaElement).value)}
            placeholder={"api.github.com\npypi.org\n10.0.0.0/8"}
            rows={4}
            className="text-sm font-mono"
          />
          <p className="mt-1 text-xs text-warning">
            Runtime whitelist support depends on your sandbox backend version.
          </p>
        </div>
      )}
    </div>
  );
}
