import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";

export function DetailPanel({
  children,
  footer,
  onSave,
  onCancel,
  onDelete,
  saveLabel,
  cancelLabel,
  deleteLabel,
  isSaving,
  isSavingLabel,
  canSave = true,
}: {
  children: ReactNode;
  footer?: ReactNode;
  onSave?: () => void;
  onCancel?: () => void;
  onDelete?: () => void;
  saveLabel?: string;
  cancelLabel?: string;
  deleteLabel?: string;
  isSaving?: boolean;
  isSavingLabel?: string;
  canSave?: boolean;
}) {
  return (
    <div className="h-full min-h-0 flex flex-col">
      <div className="flex-1 min-h-0 overflow-y-auto p-6 flex flex-col gap-6">{children}</div>
      {footer ? (
        <div className="shrink-0 border-t border-border px-6 py-3 flex items-center justify-between gap-3 bg-card">
          {footer}
        </div>
      ) : onSave || onCancel || onDelete ? (
        <div className="shrink-0 border-t border-border px-6 py-3 flex items-center justify-between gap-3 bg-card">
          <div>
            {onDelete && (
              <Button
                onClick={onDelete}
                variant="ghost"
                size="sm"
                className="text-muted-foreground hover:text-destructive-foreground cursor-pointer duration-120"
              >
                {deleteLabel || "Delete"}
              </Button>
            )}
          </div>
          <div className="flex items-center gap-2">
            {onCancel && (
              <Button
                onClick={onCancel}
                variant="ghost"
                size="sm"
                className="cursor-pointer duration-120"
              >
                {cancelLabel || "Cancel"}
              </Button>
            )}
            {onSave && (
              <Button
                onClick={onSave}
                loading={isSaving}
                disabled={!canSave}
                variant="default"
                size="sm"
                className="cursor-pointer duration-120"
              >
                {isSaving ? isSavingLabel || "Saving..." : saveLabel || "Save"}
              </Button>
            )}
          </div>
        </div>
      ) : null}
    </div>
  );
}

export function DetailPanelHeader({
  title,
  subtitle,
  action,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="space-y-1">
        <h2 className="text-lg font-semibold tracking-tight leading-snug text-foreground">
          {title}
        </h2>
        {subtitle && <div className="text-xs text-muted-foreground">{subtitle}</div>}
      </div>
      {action && <div className="flex items-center gap-3 shrink-0">{action}</div>}
    </div>
  );
}

export function FormSectionTitle({ children }: { children: ReactNode }) {
  return <p className="text-xs font-semibold text-muted-foreground">{children}</p>;
}
