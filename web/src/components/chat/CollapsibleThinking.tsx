import { ChevronDown, Lightbulb } from "lucide-react";

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
  if (!expanded) {
    return (
      <button
        onClick={() => onToggle(true)}
        className="flex items-center gap-1.5 py-0.5 transition-colors duration-120 font-mono text-xs text-muted-foreground/70 hover:text-foreground cursor-pointer w-fit"
      >
        <Lightbulb className="size-3.5 text-muted-foreground/50 shrink-0" />
        <span>{labelText}</span>
        <ChevronDown className="size-3.5 text-muted-foreground/40 shrink-0" />
      </button>
    );
  }

  return (
    <div className="space-y-2.5 max-w-3xl w-full">
      <button
        onClick={() => onToggle(false)}
        className="flex items-center gap-1.5 py-0.5 font-mono text-xs text-muted-foreground/70 hover:text-foreground cursor-pointer"
      >
        <Lightbulb className="size-3.5 text-muted-foreground/50 shrink-0" />
        <span>{labelText}</span>
        <ChevronDown className="size-3.5 text-muted-foreground/40 rotate-180" />
      </button>
      <div className="space-y-3 pl-5">{children}</div>
    </div>
  );
}
