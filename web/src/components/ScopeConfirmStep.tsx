import { ChevronLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Radio, RadioGroup } from "@/components/ui/radio-group";
import { useI18n } from "@/lib/i18n";

export interface ScopeOption<T extends string> {
  value: T;
  label: string;
  description: string;
}

/**
 * The last step before a write that decides *who else* gets what is being
 * installed: one row per scope, each carrying its full description so the
 * compound names ("Mine · this agent") explain themselves at the moment of the
 * decision rather than in a control nobody reads.
 *
 * It covers the surface it lives in (`absolute inset-0`) instead of replacing
 * it, so the view behind keeps its state — cancelling returns the user to the
 * fields they typed, not to a blank form. That also keeps it out of overlay
 * nesting, which `web-ui.md` forbids inside a Sheet.
 */
export function ScopeConfirmStep<T extends string>({
  title,
  subtitle,
  options,
  value,
  onValueChange,
  confirmLabel,
  busy,
  onConfirm,
  onCancel,
}: {
  title: string;
  subtitle: string;
  options: ScopeOption<T>[];
  value: T;
  onValueChange: (value: T) => void;
  confirmLabel: string;
  busy: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="absolute inset-0 z-10 flex flex-col bg-background">
      <div className="flex shrink-0 items-center gap-2 border-b p-4">
        <Button
          variant="ghost"
          size="icon-sm"
          disabled={busy}
          aria-label={t("common.back")}
          onClick={onCancel}
        >
          <ChevronLeft size={16} />
        </Button>
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold">{title}</p>
          <p className="truncate text-xs text-muted-foreground">{subtitle}</p>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-5">
        <RadioGroup
          value={value}
          onValueChange={(next) => onValueChange(next as T)}
          aria-label={title}
        >
          {options.map((option) => (
            <label key={option.value} className="flex cursor-pointer items-start gap-3">
              <Radio value={option.value} disabled={busy} className="mt-0.5" />
              <span className="flex min-w-0 flex-col gap-0.5">
                <span className="text-sm font-medium">{option.label}</span>
                <span className="text-xs text-muted-foreground">{option.description}</span>
              </span>
            </label>
          ))}
        </RadioGroup>
      </div>
      <div className="flex shrink-0 items-center justify-end gap-2 border-t p-4">
        <Button variant="ghost" disabled={busy} onClick={onCancel}>
          {t("common.cancel")}
        </Button>
        <Button loading={busy} onClick={onConfirm}>
          {confirmLabel}
        </Button>
      </div>
    </div>
  );
}
