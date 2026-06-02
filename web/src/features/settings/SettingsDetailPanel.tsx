import type { ReactNode } from "react";

export function DetailPanel({ children, footer }: { children: ReactNode; footer?: ReactNode }) {
  return (
    <div className="h-full overflow-y-auto">
      <div className="p-6 space-y-6">{children}</div>
      {footer && (
        <div className="border-t border-border px-6 py-3 flex items-center justify-between gap-3">
          {footer}
        </div>
      )}
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
        <h2 className="text-[1.25rem] font-semibold tracking-tight leading-snug text-foreground/90">
          {title}
        </h2>
        {subtitle && <div className="text-xs text-muted-foreground">{subtitle}</div>}
      </div>
      {action && <div className="flex items-center gap-3 shrink-0">{action}</div>}
    </div>
  );
}

export function FormSectionTitle({ children }: { children: ReactNode }) {
  return (
    <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">{children}</p>
  );
}
