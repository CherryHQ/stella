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
    <div className="flex h-full items-center justify-center p-8 text-center bg-background">
      <div className="max-w-md w-full border border-border rounded-xl p-8 bg-card">
        {icon && (
          <div className="mx-auto mb-4 flex size-12 items-center justify-center rounded-full bg-accent text-primary">
            {icon}
          </div>
        )}
        <h3 className="text-sm font-semibold text-foreground font-sans tracking-tight">
          {message}
        </h3>
        {description && (
          <p className="text-xs text-muted-foreground mt-2 max-w-72 mx-auto leading-relaxed">
            {description}
          </p>
        )}
        {action && <div className="mt-5">{action}</div>}
      </div>
    </div>
  );
}
