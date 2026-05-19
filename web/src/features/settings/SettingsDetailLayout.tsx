import { useEffect, useState } from "react";
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
  const hasDetail = detail !== undefined;
  const [mobileView, setMobileView] = useState<"list" | "detail">(hasDetail ? "detail" : "list");

  useEffect(() => {
    setMobileView(hasDetail ? "detail" : "list");
  }, [hasDetail]);

  return (
    <div className="flex h-full overflow-hidden">
      {/* Left list panel — full width on mobile when mobileView=list, hidden otherwise */}
      <div
        className={`${mobileView === "list" ? "flex" : "hidden"} md:flex w-full md:w-[220px] shrink-0 border-r border-border bg-sidebar overflow-y-auto flex-col`}
      >
        <div className="shrink-0">{listHeader}</div>
        <div className="flex-1 overflow-y-auto">{list}</div>
      </div>

      {/* Right detail panel — full width on mobile when mobileView=detail, hidden otherwise */}
      <div
        className={`${mobileView === "detail" ? "flex" : "hidden"} md:flex flex-1 flex-col overflow-hidden bg-background`}
      >
        {/* Mobile back button */}
        <button
          onClick={() => setMobileView("list")}
          className="md:hidden shrink-0 flex items-center gap-1 px-4 py-2 text-sm text-muted-foreground border-b border-border hover:text-foreground"
        >
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            className="w-4 h-4"
          >
            <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 19.5 8.25 12l7.5-7.5" />
          </svg>
          Back
        </button>

        <div className="flex-1 overflow-y-auto">
          {detail ??
            (emptyState ? (
              <div className="flex h-full items-center justify-center">{emptyState}</div>
            ) : null)}
        </div>
      </div>
    </div>
  );
}
