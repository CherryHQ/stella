import type { ReactNode } from "react";

export function DetailPanel({ children, footer }: { children: ReactNode; footer?: ReactNode }) {
  return (
    <div className="flex flex-col h-full">
      <div className="flex-1 overflow-y-auto p-6 space-y-6">{children}</div>
      {footer && (
        <div className="shrink-0 border-t border-border px-6 py-3 flex items-center justify-between gap-3 bg-background">
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
      <div>
        <h2 className="font-serif text-xl tracking-tight">{title}</h2>
        {subtitle && <div className="mt-1">{subtitle}</div>}
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
