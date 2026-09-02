import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Check } from "lucide-react";
import type { ReactNode } from "react";
import { useI18n } from "@/lib/i18n";

/**
 * The generic marketplace row: clickable upper block (title, version, badge
 * slot, description) plus a footer with meta slots and the install control.
 * The card body and the install control are siblings, never nested: only the
 * upper block opens the detail view, the footer owns its own actions.
 */
export function MarketCard({
  title,
  monoTitle = true,
  version,
  description,
  badge,
  authorChip,
  footerMeta,
  installed,
  installing,
  installDisabled,
  onOpen,
  onInstall,
}: {
  title: string;
  monoTitle?: boolean;
  version?: string | null;
  description?: string | null;
  /** Slot after the title for a feature-specific badge (auth type, transport…). */
  badge?: ReactNode;
  /** Slot in the footer for an author/attribution chip. */
  authorChip?: ReactNode;
  /** Slot in the footer before the install control (stats, transport chip…). */
  footerMeta?: ReactNode;
  installed: boolean;
  installing: boolean;
  installDisabled: boolean;
  onOpen: () => void;
  onInstall: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-card p-4">
      <button type="button" onClick={onOpen} className="flex flex-col gap-3 text-left">
        <div className="flex items-start gap-3">
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <span
              className={
                monoTitle
                  ? "truncate font-mono text-sm font-medium"
                  : "truncate text-sm font-medium"
              }
            >
              {title}
            </span>
            {version && (
              <Badge variant="outline" size="sm">
                v{version}
              </Badge>
            )}
            {badge}
          </div>
          {installed && (
            <Badge variant="success" size="sm">
              <Check />
            </Badge>
          )}
        </div>
        {description && <p className="line-clamp-2 text-xs text-muted-foreground">{description}</p>}
      </button>
      <div className="mt-auto flex items-center gap-3 border-t pt-3 text-xs text-muted-foreground">
        {footerMeta}
        {authorChip}
        <span className="ml-auto">
          {installed ? (
            <Button size="xs" variant="ghost" disabled>
              {t("sessions.discover.installed")}
            </Button>
          ) : (
            <Button size="xs" loading={installing} disabled={installDisabled} onClick={onInstall}>
              {t("common.install")}
            </Button>
          )}
        </span>
      </div>
    </div>
  );
}
