"use client";

import { useCallback, useSyncExternalStore } from "react";

const BREAKPOINTS = {
  "2xl": 1536,
  "3xl": 1600,
  "4xl": 2000,
  lg: 1024,
  md: 800,
  sm: 640,
  xl: 1280,
} as const;

type Breakpoint = keyof typeof BREAKPOINTS;

type BreakpointQuery = Breakpoint | `max-${Breakpoint}` | `${Breakpoint}:max-${Breakpoint}`;

function isNumber(value: Breakpoint | number): value is number {
  // The input union contains only primitive strings and numbers, including non-finite numbers.
  return Number.isFinite(value) || Number.isNaN(value) || value === Infinity || value === -Infinity;
}

function isMediaQueryInput(
  value: BreakpointQuery | MediaQueryInput | (string & {}),
): value is MediaQueryInput {
  return value !== null && Object(value) === value;
}

function resolveMin(value: Breakpoint | number): string {
  const px = isNumber(value) ? value : BREAKPOINTS[value];
  return `(min-width: ${px}px)`;
}

function resolveMax(value: Breakpoint | number): string {
  const px = isNumber(value) ? value : BREAKPOINTS[value];
  return `(max-width: ${px - 1}px)`;
}

function parseQuery(query: BreakpointQuery | MediaQueryInput | (string & {})): string {
  if (isMediaQueryInput(query)) {
    const parts: string[] = [];
    if (query.min != null) parts.push(resolveMin(query.min));
    if (query.max != null) parts.push(resolveMax(query.max));
    if (query.pointer === "coarse") parts.push("(pointer: coarse)");
    if (query.pointer === "fine") parts.push("(pointer: fine)");
    if (parts.length === 0) return "(min-width: 0px)";
    return parts.join(" and ");
  }

  if (query.startsWith("(")) return query;

  const parts: string[] = [];
  for (const segment of query.split(":")) {
    if (segment.startsWith("max-")) {
      const bp = segment.slice(4);
      if (bp in BREAKPOINTS) {
        // SAFETY: membership in BREAKPOINTS was checked on this narrow key.
        parts.push(resolveMax(bp as Breakpoint));
      }
    } else {
      if (segment in BREAKPOINTS) {
        // SAFETY: membership in BREAKPOINTS was checked on this segment key.
        parts.push(resolveMin(segment as Breakpoint));
      }
    }
  }

  return parts.length > 0 ? parts.join(" and ") : query;
}

function getServerSnapshot(): boolean {
  return false;
}

export type MediaQueryInput = {
  min?: Breakpoint | number;
  max?: Breakpoint | number;
  /** Touch-like input (finger). Use "fine" for mouse/trackpad. */
  pointer?: "coarse" | "fine";
};

export function useMediaQuery(query: BreakpointQuery | MediaQueryInput | (string & {})): boolean {
  const mediaQuery = parseQuery(query);

  const subscribe = useCallback(
    (callback: () => void) => {
      const browserWindow = globalThis.window;
      if (!browserWindow) return () => {};
      const mql = browserWindow.matchMedia(mediaQuery);
      mql.addEventListener("change", callback);
      return () => mql.removeEventListener("change", callback);
    },
    [mediaQuery],
  );

  const getSnapshot = useCallback(() => {
    const browserWindow = globalThis.window;
    if (!browserWindow) return false;
    return browserWindow.matchMedia(mediaQuery).matches;
  }, [mediaQuery]);

  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function useIsMobile(): boolean {
  return useMediaQuery("max-md");
}
