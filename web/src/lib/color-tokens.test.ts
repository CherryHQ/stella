import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * Guards the one failure mode the documented token scan cannot see.
 *
 * `web-theming.md` greps for hardcoded colors, so it catches `bg-[#0f0]`. It
 * cannot catch `bg-success` when `--color-success` was never bridged: the class
 * is contract-compliant, reviews read it as correct, and Tailwind silently emits
 * no rule at all. Three vendored primitives and thirteen call sites shipped that
 * way — every success toast rendered as text on no background.
 *
 * Scope, deliberately narrow so this never goes flaky: the `-foreground` suffix
 * belongs to this design system alone and collides with nothing in Tailwind's
 * vocabulary, so every `*-foreground` reference is known to be a color. That is
 * enough to catch this bug's shape — a semantic pair where one or both halves
 * were never defined. It does not police single tokens with no `-foreground`
 * partner; a full utility-to-CSS diff belongs in the build, not a unit test.
 */

const SRC = join(import.meta.dirname, "..");

// `border`/`divide` take an optional side qualifier before the color
// (`border-t-muted-foreground`), so it has to be stripped or it lands in the
// captured token name.
const FOREGROUND_UTILITY =
  /\b(?:bg|text|ring|fill|stroke|from|via|to|caret|accent|(?:border|divide)(?:-[trblxyse])?)-([a-z][a-z0-9-]*-foreground)(?:\/\d+)?\b/g;

const SOURCE = /\.tsx?$/;
const STYLES = /\.css$/;

function walk(dir: string, match: RegExp = SOURCE, out: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "api-client") continue; // generated
      walk(path, match, out);
    } else if (match.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
      out.push(path);
    }
  }
  return out;
}

function definedColorTokens(): Set<string> {
  const css = readFileSync(join(SRC, "globals.css"), "utf8");
  const names = new Set<string>();
  for (const m of css.matchAll(/--color-([a-z0-9-]+):/g)) names.add(m[1]);
  return names;
}

describe("semantic color pairs resolve to defined theme tokens", () => {
  it("bridges every *-foreground token referenced in source, and its base", () => {
    const defined = definedColorTokens();
    const offenders = new Set<string>();

    for (const file of walk(SRC)) {
      const source = readFileSync(file, "utf8");
      for (const match of source.matchAll(FOREGROUND_UTILITY)) {
        const foreground = match[1];
        const base = foreground.replace(/-foreground$/, "");
        const where = file.slice(SRC.length + 1);
        if (!defined.has(foreground)) offenders.add(`${where}: --color-${foreground} undefined`);
        if (!defined.has(base)) offenders.add(`${where}: --color-${base} undefined`);
      }
    }

    expect([...offenders]).toEqual([]);
  });

  it("keeps the plot palette out of anything that gets read", () => {
    // `chart-*` is tuned to be plotted: filled areas, where lightness carries
    // category rather than legibility. Light mode measures 2.35:1 for `chart-4`
    // as copy on a card and 2.19:1 for the same token as a status dot, missing
    // the 4.5:1 text floor and the 3:1 non-text floor respectively.
    //
    // `bg-chart-*` is deliberately still allowed: an agent avatar and a scope
    // rail are categories, which is what these tokens mean, and both sit beside
    // a text label that carries the same information. Coloring a *word* with
    // one has no such out — the word is the content.
    const offenders: string[] = [];
    for (const file of walk(SRC)) {
      const source = readFileSync(file, "utf8");
      for (const m of source.matchAll(/\b(?:text|fill|stroke)-chart-\d\b/g)) {
        offenders.push(
          `${file.slice(SRC.length + 1)}: ${m[0]} (use a status token, or --foreground)`,
        );
      }
    }

    // The same mistake in plain CSS, where the class names do not apply. The
    // lookbehind keeps `background-color:` out of it — a chart token as a fill
    // is the correct use.
    for (const file of walk(SRC, STYLES)) {
      const source = readFileSync(file, "utf8");
      for (const m of source.matchAll(/(?<![-\w])color:\s*var\(--chart-\d\)/g)) {
        offenders.push(`${file.slice(SRC.length + 1)}: ${m[0]} (use a status token)`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it("keeps red words off the red fill token", () => {
    // `--destructive` is a fill under a white label CossUI hardcodes, so it is
    // tuned dark; `--destructive-foreground` is the red you read on the page.
    // Writing `text-destructive` looks right and is the more obvious name, which
    // is why 61 call sites had it. Nothing on screen reports the difference —
    // in dark mode it is the gap between 5.66:1 and 3.2:1.
    //
    // CossUI is vendored and regenerated, so its three remaining uses are named
    // here rather than skipped wholesale: an exception the next `coss add` can
    // reintroduce silently is not an exception, it is a blind spot. All three
    // are icons, which owe 3:1 and clear it — they are a naming inconsistency
    // with the docs, not a failure. Pinning the count is what makes the docs'
    // "every red word in this app" claim checkable.
    const VENDORED_ALLOWANCE = new Map([
      [join("components", "ui", "alert.tsx"), 1],
      [join("components", "ui", "toast.tsx"), 2],
    ]);

    const offenders: string[] = [];
    const seen = new Map<string, number>();
    for (const file of walk(SRC)) {
      const where = file.slice(SRC.length + 1);
      const source = readFileSync(file, "utf8");
      const hits = [...source.matchAll(/\btext-destructive(?!-foreground)\b/g)].length;
      if (!hits) continue;
      seen.set(where, hits);
      if (VENDORED_ALLOWANCE.get(where) === hits) continue;
      offenders.push(`${where}: ${hits}x text-destructive (use text-destructive-foreground)`);
    }
    expect(offenders).toEqual([]);

    // And the allowance itself has to stay honest: a vendored file that stopped
    // using it should drop off the list rather than sit here forever.
    expect([...seen.keys()].sort()).toEqual([...VENDORED_ALLOWANCE.keys()].sort());
  });

  it("defines the status pairs the vendored CossUI variants consume", () => {
    const defined = definedColorTokens();
    // badge.tsx, alert.tsx and toast.tsx ship success/warning/info variants that
    // no Stella feature asked for. They arrived with CossUI, so the next CossUI
    // sync is exactly where this regresses.
    for (const name of ["success", "warning", "info", "destructive"]) {
      expect(defined, `--color-${name} must be bridged in globals.css`).toContain(name);
      expect(defined, `--color-${name}-foreground must be bridged`).toContain(`${name}-foreground`);
    }
  });
});
