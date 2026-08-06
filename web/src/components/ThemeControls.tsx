import { useEffect, useState } from "react";
import { Monitor, Moon, Sun, type LucideIcon } from "lucide-react";
import { Separator } from "@/components/ui/separator";
import { Slider } from "@/components/ui/slider";
import { SegmentedField } from "@/components/SegmentedField";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";
import {
  applyTheme,
  getStoredTheme,
  setStoredTheme,
  accentSwatch,
  ACCENT_PRESETS,
  DEFAULT_ACCENT_HUE,
  type ThemeAppearance,
  type ThemeSettings,
} from "@/lib/theme";

export const APPEARANCE_ICONS: Record<ThemeAppearance, LucideIcon> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
};

const APPEARANCES: ThemeAppearance[] = ["system", "light", "dark"];

const APPEARANCE_LABELS: Record<ThemeAppearance, MessageKey> = {
  system: "header.system",
  light: "header.light",
  dark: "header.dark",
};

/**
 * Theme settings live in localStorage, not in React state, so every control
 * re-reads the stored value before writing. Local state exists only to re-render
 * the control that was touched.
 */
function useThemeSettings(): [ThemeSettings, (next: ThemeSettings) => void] {
  const [theme, setTheme] = useState<ThemeSettings>(() => getStoredTheme());
  return [
    theme,
    (next: ThemeSettings) => {
      setTheme(next);
      setStoredTheme(next);
    },
  ];
}

/**
 * Light / dark / system picker.
 *
 * `layout="stacked"` is the roomy settings-surface form (icon above label);
 * `layout="inline"` is the one-row form that fits inside a dropdown menu.
 */
export function ThemeAppearanceControl({ layout = "stacked" }: { layout?: "stacked" | "inline" }) {
  const { t } = useI18n();
  const [theme, update] = useThemeSettings();

  useEffect(() => {
    if (theme.appearance !== "system") return;

    const media = window.matchMedia("(prefers-color-scheme: dark)");
    // Re-read full settings so a system-theme flip still honors the chosen accent.
    const onChange = () => applyTheme(getStoredTheme());
    media.addEventListener("change", onChange);

    return () => media.removeEventListener("change", onChange);
  }, [theme]);

  function select(appearance: ThemeAppearance) {
    update({ ...getStoredTheme(), appearance });
  }

  // Inline is the menu row: one label, one segmented control, icon-only because
  // three worded segments do not fit a 16rem dropdown in every locale.
  if (layout === "inline") {
    return (
      <SegmentedField
        label={t("header.appearance")}
        value={theme.appearance}
        onChange={select}
        options={APPEARANCES.map((appearance) => ({
          value: appearance,
          label: t(APPEARANCE_LABELS[appearance]),
          icon: APPEARANCE_ICONS[appearance],
        }))}
      />
    );
  }

  return (
    <div className="flex flex-col gap-2.5">
      <span className="px-0.5 text-xs font-medium text-muted-foreground">
        {t("header.appearance")}
      </span>
      <div className="grid grid-cols-3 gap-1.5 rounded-xl bg-muted p-1.5">
        {APPEARANCES.map((appearance) => {
          const ItemIcon = APPEARANCE_ICONS[appearance];
          const active = theme.appearance === appearance;
          const label = t(APPEARANCE_LABELS[appearance]);
          return (
            <button
              key={appearance}
              type="button"
              aria-pressed={active}
              onClick={() => select(appearance)}
              className={cn(
                "flex flex-col items-center justify-center gap-1.5 whitespace-nowrap rounded-lg py-2.5 text-xs transition-colors",
                active
                  ? "bg-card text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              <ItemIcon className="size-4 shrink-0" />
              {label}
            </button>
          );
        })}
      </div>
    </div>
  );
}

/** Accent hue presets plus a free hue slider. */
export function ThemeAccentControl() {
  const { t } = useI18n();
  const [theme, update] = useThemeSettings();

  function setHue(next: number | undefined) {
    // Treat the default teal hue as "unset" so we fall back to tokens.css and
    // hide the reset affordance.
    const norm = next === undefined ? undefined : ((next % 360) + 360) % 360;
    const accentHue = norm === DEFAULT_ACCENT_HUE ? undefined : norm;
    update({ ...getStoredTheme(), accentHue });
  }

  const current = theme.accentHue ?? DEFAULT_ACCENT_HUE;

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between px-0.5">
        <span className="text-xs font-medium text-muted-foreground">{t("header.accent")}</span>
        {theme.accentHue !== undefined && (
          <button
            type="button"
            onClick={() => setHue(undefined)}
            className="text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            {t("header.resetAccent")}
          </button>
        )}
      </div>
      <div className="grid grid-cols-7 gap-1.5">
        {ACCENT_PRESETS.map((p) => {
          const active = current === p.hue;
          return (
            <button
              key={p.hue}
              type="button"
              onClick={() => setHue(p.hue)}
              title={p.name}
              aria-label={p.name}
              className={cn(
                "size-6 rounded-full transition-transform hover:scale-110",
                active && "ring-2 ring-foreground/40 ring-offset-2 ring-offset-popover",
              )}
              style={{ background: accentSwatch(p.hue) }}
            />
          );
        })}
      </div>
      <div className="px-0.5 pt-1">
        <Slider
          min={0}
          max={359}
          value={current}
          onValueChange={(v) => setHue(Array.isArray(v) ? v[0] : v)}
        />
      </div>
    </div>
  );
}

// The appearance + accent controls with no surface of their own, so they can sit
// inside a popover (marketing header) or a settings section (account page)
// without ever nesting one overlay inside another.
export function ThemeControls() {
  return (
    <>
      <ThemeAppearanceControl />
      <Separator />
      <ThemeAccentControl />
    </>
  );
}
