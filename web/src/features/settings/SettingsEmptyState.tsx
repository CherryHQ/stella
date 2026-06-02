import type { ReactNode } from "react";

interface SettingsEmptyStateProps {
  icon?: ReactNode;
  message: string;
  description?: string;
  action?: ReactNode;
}

export function SettingsEmptyState({
  icon,
  message,
  description,
  action,
}: SettingsEmptyStateProps) {
  return (
    <div className="flex h-full items-center justify-center p-8 text-center">
      <div className="max-w-md w-full border border-border/40 rounded-2xl p-8 bg-card/45 backdrop-blur-md shadow-2xs">
        {icon && (
          <div className="mx-auto mb-4 flex size-12 items-center justify-center rounded-full bg-primary/10 text-primary">
            {icon}
          </div>
        )}
        <h3 className="text-sm font-semibold text-foreground/90">{message}</h3>
        {description && (
          <p className="text-xs text-muted-foreground/60 mt-1.5 max-w-72 mx-auto leading-relaxed">
            {description}
          </p>
        )}
        {action && <div className="mt-5">{action}</div>}
      </div>
    </div>
  );
}
