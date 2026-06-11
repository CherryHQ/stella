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
        className="flex items-center gap-2 px-3 py-1.5 rounded-lg border border-border bg-muted/40 hover:bg-muted/70 hover:border-foreground/10 transition-colors duration-120 font-mono text-xs text-muted-foreground hover:text-foreground cursor-pointer w-fit shadow-none"
      >
        <Lightbulb className="size-3.5 text-muted-foreground shrink-0" />
        <span>{labelText}</span>
        <ChevronDown className="size-3.5 text-muted-foreground/60 shrink-0 ml-0.5" />
      </button>
    );
  }

  return (
    <div className="rounded-xl border border-border bg-card px-4 py-3.5 transition-colors duration-120 space-y-3.5 max-w-3xl w-full shadow-none overflow-hidden">
      <div className="flex items-center">
        <button
          onClick={() => onToggle(false)}
          className="flex items-center gap-1.5 font-mono text-xs text-muted-foreground hover:text-foreground cursor-pointer font-semibold"
        >
          <Lightbulb className="size-3.5 text-muted-foreground shrink-0" />
          <span>{labelText}</span>
          <ChevronDown className="size-3.5 transition-transform duration-120 text-muted-foreground/50 rotate-180" />
        </button>
      </div>
      <div className="space-y-3 pt-3 border-t border-border/60">{children}</div>
    </div>
  );
}
