import { ChevronDown, Lightbulb } from "lucide-react";
import { useId } from "react";

export function CollapsibleThinking({
  labelText,
  expanded,
  onToggle,
  children,
}: {
  labelText: string;
  expanded: boolean;
  onToggle: (v: boolean) => void;
  children: React.ReactNode;
}) {
  const panelId = useId();

  if (!expanded) {
    return (
      <button
        type="button"
        aria-expanded={false}
        onClick={() => onToggle(true)}
        className="flex items-center gap-1.5 py-0.5 transition-colors duration-120 font-mono text-xs text-muted-foreground hover:text-foreground cursor-pointer w-fit"
      >
        <Lightbulb className="size-3.5 text-muted-foreground shrink-0" />
        <span>{labelText}</span>
        <ChevronDown className="size-3.5 text-muted-foreground shrink-0" />
      </button>
    );
  }

  return (
    <div className="space-y-2.5 max-w-3xl w-full">
      <button
        type="button"
        aria-expanded={true}
        aria-controls={panelId}
        onClick={() => onToggle(false)}
        className="flex items-center gap-1.5 py-0.5 font-mono text-xs text-muted-foreground hover:text-foreground cursor-pointer"
      >
        <Lightbulb className="size-3.5 text-muted-foreground shrink-0" />
        <span>{labelText}</span>
        <ChevronDown className="size-3.5 text-muted-foreground rotate-180" />
      </button>
      <div id={panelId} className="space-y-3 pl-5">
        {children}
      </div>
    </div>
  );
}
