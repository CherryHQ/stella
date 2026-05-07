import { normalizeSandbox } from "../AgentsPage";
import type { AgentsPageState } from "../AgentsPage";

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
    <div className="border border-base-300 rounded-box p-4 bg-base-200/40 space-y-4">
      <div>
        <p className="text-xs font-mono font-medium text-secondary uppercase tracking-wider mb-1">
          Network Policy
        </p>
        <p className="text-xs text-base-content/60">
          Sandbox backend is configured on the{" "}
          <a href="/plugins" className="link link-primary">Plugins</a> page.
        </p>
      </div>
      <div>
        <label className="label"><span className="label-text font-mono text-sm">Network Mode</span></label>
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
          className="select select-bordered w-full text-sm"
        >
          <option value="disabled">disabled — block outbound network</option>
          <option value="allow_all">allow_all — allow outbound network</option>
          <option value="whitelist">whitelist — only listed hosts/CIDRs</option>
        </select>
      </div>
      {networkMode === "whitelist" && (
        <div>
          <label className="label"><span className="label-text font-mono text-sm">Allowlist</span></label>
          <textarea
            value={allowlistText}
            onChange={(e) => updateSandboxAllowlist(e.target.value)}
            placeholder={"api.github.com\npypi.org\n10.0.0.0/8"}
            rows={4}
            className="textarea textarea-bordered w-full text-sm font-mono resize-y"
          />
          <p className="mt-1 text-xs text-warning">
            Runtime whitelist support depends on your sandbox backend version.
          </p>
        </div>
      )}
    </div>
  );
}
