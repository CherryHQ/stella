import { normalizeSandbox } from "@/lib/queries/agent-settings";
import { targetValue } from "@/lib/utils";
import type { AgentsPageState } from "../agent-detail-state";
import { Textarea } from "@/components/ui/textarea";
import { useI18n } from "@/lib/i18n";

interface Props {
  state: AgentsPageState;
  /** False when the viewer may read the agent but not manage it. */
  canEdit: boolean;
  onSetState: (patch: Partial<AgentsPageState>) => void;
}

export function AdvancedTab({ state, canEdit, onSetState }: Props) {
  const { t } = useI18n();
  const { form } = state;

  const setForm = (patch: Partial<typeof form>) => onSetState({ form: { ...form, ...patch } });

  const allowlistText = (form.sandbox?.network?.allowlist ?? []).join("\n");

  const updateSandboxAllowlist = (value: string) => {
    setForm({
      sandbox: normalizeSandbox({
        network: {
          mode: form.sandbox?.network?.mode ?? "allow_all",
          allowlist: value
            .split(/\r?\n|,/)
            .map((v) => v.trim())
            .filter(Boolean),
        },
      }),
    });
  };

  const networkMode = form.sandbox?.network?.mode ?? "allow_all";

  return (
    <div className="space-y-6">
      <div>
        <p className="text-xs font-semibold text-muted-foreground mb-1.5">
          {t("agents.form.networkPolicy")}
        </p>
        <p className="text-xs text-muted-foreground">
          Sandbox backend is configured on the{" "}
          <a
            href="/admin/integrations/plugins"
            className="text-primary underline underline-offset-4 font-medium transition-colors hover:text-primary/80"
          >
            Plugins
          </a>{" "}
          page.
        </p>
      </div>
      <div>
        <label className="block text-xs font-semibold text-muted-foreground mb-1.5">
          {t("agents.form.networkMode")}
        </label>
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
          disabled={!canEdit}
          className="w-full rounded-lg border border-border bg-background px-3 py-2 text-sm outline-none focus:border-primary focus:ring-2 focus:ring-primary/20 cursor-pointer text-foreground font-medium disabled:cursor-not-allowed disabled:opacity-60"
        >
          <option value="disabled">disabled — block outbound network</option>
          <option value="allow_all">allow_all — allow outbound network</option>
          <option value="whitelist">whitelist — only listed hosts/CIDRs</option>
        </select>
      </div>
      {networkMode === "whitelist" && (
        <div>
          <label className="block text-xs font-semibold text-muted-foreground mb-1.5">
            {t("agents.form.allowlist")}
          </label>
          <Textarea
            value={allowlistText}
            onChange={(e) => updateSandboxAllowlist(targetValue(e))}
            placeholder={"api.github.com\npypi.org\n10.0.0.0/8"}
            rows={4}
            disabled={!canEdit}
            className="text-sm font-mono"
          />
          <p className="mt-1.5 text-xs text-warning">{t("agents.form.allowlistHint")}</p>
        </div>
      )}
    </div>
  );
}
