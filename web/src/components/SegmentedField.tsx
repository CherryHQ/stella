import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

export interface SegmentedOption<T extends string> {
  value: T;
  /** The accessible name, and the visible text when `icon` is absent. */
  label: string;
  icon?: LucideIcon;
}

/**
 * One preference stated as a label and a compact segmented control on the same
 * row.
 *
 * This is the shape a menu can afford: every choice is on screen, one click
 * commits it, and the current value is visible rather than inferred from the
 * label of the thing that would change it. A control that needs exploring — a
 * hue wheel, a slider, anything with a preview — belongs on a settings surface,
 * not here.
 *
 * The segmented idiom is the sidebar's own (see the app switcher in
 * `AppChromeHeader`): a `muted` track with the active segment raised onto
 * `card`. Deliberately not CossUI's ToggleGroup — its pressed state reads as a
 * button held down rather than a segment chosen, and it would put a second
 * segmented language inside the same sidebar column.
 */
export function SegmentedField<T extends string>({
  label,
  options,
  value,
  onChange,
  className,
}: {
  label: string;
  options: SegmentedOption<T>[];
  value: T;
  onChange: (next: T) => void;
  className?: string;
}) {
  return (
    <div className={cn("flex items-center justify-between gap-3", className)}>
      <span className="px-0.5 text-xs font-medium text-muted-foreground">{label}</span>
      <div className="flex shrink-0 items-center gap-0.5 rounded-lg bg-muted p-0.5">
        {options.map((option) => {
          const Icon = option.icon;
          const active = option.value === value;
          return (
            <button
              key={option.value}
              type="button"
              aria-pressed={active}
              // An icon segment has no text to name it; a worded one already
              // says its name, and repeating it only doubles what gets read out.
              aria-label={Icon ? option.label : undefined}
              title={Icon ? option.label : undefined}
              onClick={() => onChange(option.value)}
              className={cn(
                "flex items-center justify-center whitespace-nowrap rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
                active
                  ? "bg-card text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {Icon ? <Icon className="size-4 shrink-0" /> : option.label}
            </button>
          );
        })}
      </div>
    </div>
  );
}
