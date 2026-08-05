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

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "api-client") continue; // generated
      walk(path, out);
    } else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
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

  it("keeps red words off the red fill token", () => {
    // `--destructive` is a fill under a white label CossUI hardcodes, so it is
    // tuned dark; `--destructive-foreground` is the red you read on the page.
    // Writing `text-destructive` looks right and is the more obvious name, which
    // is why 61 call sites had it. Nothing on screen reports the difference —
    // in dark mode it is the gap between 5.66:1 and 3.2:1.
    const offenders: string[] = [];
    for (const file of walk(SRC)) {
      if (file.includes(join("components", "ui"))) continue; // vendored, read-only
      const source = readFileSync(file, "utf8");
      for (const m of source.matchAll(/\btext-destructive(?!-foreground)\b/g)) {
        offenders.push(`${file.slice(SRC.length + 1)}: ${m[0]} (use text-destructive-foreground)`);
      }
    }
    expect(offenders).toEqual([]);
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
