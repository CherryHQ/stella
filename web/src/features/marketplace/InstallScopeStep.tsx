import { useState } from "react";
import { ScopeConfirmStep } from "@/components/ScopeConfirmStep";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import {
  INSTALL_SCOPES,
  SCOPE_DESC_KEY,
  SCOPE_LABEL_KEY,
  type SkillScope,
} from "@/lib/skill-scope";

export type InstallScope = (typeof INSTALL_SCOPES)[number];

// Every writable owner·range destination; hosts with a wider set than skills
// (the MCP marketplace can target bare system) instantiate the step with it.
export type WritableScope = Extract<SkillScope, "user" | "user_agent" | "system" | "system_agent">;

// One pending write, deferred until the user confirms a destination. `run`
// owns the request (and its own error reporting) and reports success so the
// confirmation step knows whether it may dismiss itself.
export type InstallRequest<S extends WritableScope = InstallScope> = {
  name?: string;
  confirmLabel: string;
  run: (scope: S) => Promise<boolean>;
};

/**
 * The install destination is confirmed per install, never left standing: the
 * radio list is the shared owner·range vocabulary, and no write happens
 * without a just-confirmed destination. `showAgentScope` gates the
 * administrator-only system_agent option exactly as the backend gates it. A
 * host with a different destination set (the MCP marketplace) passes `scopes`
 * explicitly.
 */
export function InstallScopeStep<S extends WritableScope = InstallScope>({
  request,
  defaultScope,
  showAgentScope,
  scopes,
  onConfirmed,
  onCancel,
}: {
  request: InstallRequest<S>;
  defaultScope: S;
  showAgentScope: boolean;
  scopes?: S[];
  onConfirmed: (scope: S) => void;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const [scope, setScope] = useState<S>(defaultScope);
  const [busy, setBusy] = useState(false);
  // SAFETY: without an explicit list the host is the skills sheet, whose S is
  // InstallScope, so the skills default list is a valid S[].
  const options: S[] =
    scopes ?? (INSTALL_SCOPES.filter((s) => s !== "system_agent" || showAgentScope) as S[]);

  async function confirm() {
    setBusy(true);
    try {
      // Stay open on failure — the caller's toast already said why.
      if (await request.run(scope)) onConfirmed(scope);
    } finally {
      setBusy(false);
    }
  }

  return (
    <ScopeConfirmStep
      title={t("sessions.discover.installWhere")}
      subtitle={
        request.name
          ? t("sessions.discover.installingName", { name: request.name })
          : t("sessions.discover.installWhereDesc")
      }
      options={options.map((s) => {
        // Widen the generic index to MessageKey so t() resolves to string.
        const labelKey: MessageKey = SCOPE_LABEL_KEY[s];
        const descKey: MessageKey = SCOPE_DESC_KEY[s];
        return { value: s, label: t(labelKey), description: t(descKey) };
      })}
      value={scope}
      onValueChange={setScope}
      confirmLabel={request.confirmLabel}
      busy={busy}
      onConfirm={() => void confirm()}
      onCancel={onCancel}
    />
  );
}
