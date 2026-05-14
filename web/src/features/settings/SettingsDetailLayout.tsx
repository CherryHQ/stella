import type { ReactNode } from "react";

interface SettingsDetailLayoutProps {
  listHeader: ReactNode;
  list: ReactNode;
  detail?: ReactNode;
  emptyState?: ReactNode;
}

export function SettingsDetailLayout({
  listHeader,
  list,
  detail,
  emptyState,
}: SettingsDetailLayoutProps) {
  return (
    <div className="flex h-full overflow-hidden">
      {/* Left panel */}
      <div className="w-[200px] shrink-0 border-r border-border bg-muted/30 overflow-y-auto flex flex-col">
        <div className="shrink-0">{listHeader}</div>
        <div className="flex-1 overflow-y-auto">{list}</div>
      </div>

      {/* Right detail panel */}
      <div className="flex-1 overflow-y-auto bg-background">
        {detail ??
          (emptyState ? (
            <div className="flex h-full items-center justify-center">{emptyState}</div>
          ) : null)}
      </div>
    </div>
  );
}
