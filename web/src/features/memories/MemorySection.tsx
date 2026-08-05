import { type ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";

interface Props {
  title: string;
  description?: string;
  count?: number;
  defaultOpen?: boolean;
  action?: ReactNode;
  children: ReactNode;
}

export function MemorySection({
  title,
  description,
  count,
  defaultOpen = false,
  action,
  children,
}: Props) {
  return (
    <Collapsible defaultOpen={defaultOpen}>
      <div className="border-b border-border">
        <div className="flex items-center justify-between gap-3 py-3">
          <CollapsibleTrigger className="flex flex-1 items-center gap-2 py-1 text-left cursor-pointer group">
            <ChevronRight className="size-3.5 text-muted-foreground transition-transform duration-150 ease-out group-data-[panel-open]:rotate-90" />
            <div className="min-w-0">
              {/* Same heading grammar as the other profile tabs
                  (`ProfilePanelSection`): title, then a muted count. */}
              <span className="text-sm font-semibold">
                {title}
                {count != null && count > 0 && (
                  <span className="ml-2 text-xs font-normal text-muted-foreground">{count}</span>
                )}
              </span>
              {description && (
                <span className="ml-2 text-xs text-muted-foreground hidden group-data-[panel-open]:hidden sm:inline">
                  {description}
                </span>
              )}
            </div>
          </CollapsibleTrigger>
          {action && <div className="shrink-0">{action}</div>}
        </div>
      </div>
      <CollapsiblePanel>
        <div className="py-4">{children}</div>
      </CollapsiblePanel>
    </Collapsible>
  );
}
