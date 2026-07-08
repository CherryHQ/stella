---
title: Web UI engineering
description: Theme-independent engineering rules for building UI in Stella's web app.
---

Theme-independent rules for building UI in `web/`. They survive any visual restyle. The current theme lives in [`web-design.md`](./web-design.md); the restyle procedure in [`web-theming.md`](./web-theming.md).

## Design tokens

The token pipeline: `web/src/tokens.css` defines values → `globals.css` `@theme inline` maps them to Tailwind utilities → components consume semantic classes. This is what makes theme swaps a two-file change — every rule below protects that contract.

- Always use semantic Tailwind utilities: `bg-background`, `text-foreground`, `text-primary`, `bg-muted`, `border-border`, etc.
- Never hardcode color values: no `text-[#abc]`, no `bg-[oklch(...)]`, no inline `style={{ color }}`, no palette classes like `bg-violet-500`.
- Never reference `var(--some-color)` directly in JSX when a Tailwind utility exists.
- Never use arbitrary spacing values like `p-[13px]`. Stick to the 4px grid; prefer `gap-*` on flex/grid parents over margin on children.
- Do not add project-specific color aliases for one-off states or surfaces. If shadcn lacks an exact semantic name, use the closest existing token; status colors map to `chart-3` success, `chart-4` warning, `chart-2` info/running, and `destructive` error.

## Dark mode

Theme is class-based: `.dark` on `<html>`, managed by `src/lib/theme.ts` (user-selectable: light / dark / system).

- Never use `dark:` variants to override semantic tokens — they switch automatically via `tokens.css`.
- Use `dark:` only for non-token properties (e.g., `dark:opacity-80` on images).
- Test both modes. A component that looks correct in one mode but broken in the other is a bug.

## CossUI — zero custom styling

Always use CossUI primitives from `src/components/ui/` (53+ components on `@base-ui/react`). Hard rules:

- **Never hand-write a primitive that CossUI covers.** Button, Dialog, Select, Card, Tabs, etc. — no exceptions.
- **Never override CossUI visual styles** (colors, radius, shadows, padding, font) inline or with extra Tailwind classes. Use variant/size props. If a variant doesn't exist, propose adding it to the CossUI component — don't patch the call site.
- **Never create a one-off styled wrapper** around a CossUI component. If a wrapper only adds Tailwind classes, those classes belong in the component's variant system or in the theme.
- **Compose, don't customize.** A settings form is Field + Input + Select + Button composed — not a custom `<SettingsInput>` with bespoke styles.
- **Feature components inherit, not override.** The only Tailwind classes allowed on feature components are layout concerns: `flex`, `grid`, `gap-*`, `p-*`, `w-*`, `h-*`, `overflow-*`. Color, typography, radius, and shadow come from CossUI and the theme.

**Why:** when the theme changes (swap `tokens.css` + `web-design.md`), every page must update automatically. A component that hardcodes palette colors or radii breaks this contract and becomes a manual migration target.

Button variants: `default` (primary action — one per view), `secondary`, `ghost` (toolbar/sidebar), `outline`, `destructive`, `link`, `premium`/`premium-outline` (paid features).

Read `web/.agents/skills/coss/SKILL.md` for imports, composition, and particle examples.

## Layout shell

SiteHeader (h-14, border-b) over Sidebar (16rem desktop, 18rem mobile, 3rem collapsed icon-only, offcanvas modal on mobile) + content inset with sub-header (h-12, backdrop-blur). Split-pane views use CSS `flex` with a draggable divider (not Grid), min-width constraints, and fall back to Sheet on mobile. Mobile breakpoint: `max-md` (< 768px); multi-column layouts collapse to single column.

## Overlay decision tree

| Need                                     | Component                    | Example                          |
| ---------------------------------------- | ---------------------------- | -------------------------------- |
| Blocking confirmation or multi-step form | **Dialog** (`DialogPopup`)   | Create group, confirm delete     |
| Side panel with detail/inspector content | **Sheet** (`SheetPopup`)     | Mobile inspector, session detail |
| Small contextual options from a trigger  | **Popover** (`PopoverPopup`) | Color picker, quick settings     |
| Action menu from a trigger               | **Menu** (`DropdownMenu`)    | Row actions, user menu           |

On mobile, replace side panels with Sheet (bottom or right), not Dialog. Prefer inline resolution over spawning a blocking Dialog. Never nest overlays — a Dialog inside a Sheet is a bug.

## Feedback patterns

- **Toasts:** use `useToast()` from `@/hooks/use-toast` (not a shadcn toast). Kinds: `"success" | "error"`, default 3000ms.
- **Loading:** TanStack Query's `isLoading` with a Spinner or brief text — never a full-page skeleton unless the page is data-heavy.
- **Empty states:** the shared `SettingsEmptyState` (`@/features/settings/SettingsEmptyState`).
- **Errors:** display inline, direct tone, no apology.

## Form validation

No form library (no Zod, no React Hook Form). Plain `useState` per field with inline validation; errors via Field's error slot. Wrap inputs in `<Field>`, group with `<Fieldset>`.

## Z-index layers

Sticky headers `z-30`, resizable dividers `z-10`, overlays managed by CossUI (don't override), toasts `z-[9999]`. Don't invent arbitrary z-index values — extend this list if a new layer is needed.

## Icons

`lucide-react` only. Sizes 16px and 20px. In buttons with text: `size={16}` with `gap-2`; standalone: `size="icon-sm"`/`size="icon"`. All UI icons monochrome at `text-muted-foreground`. Never use emoji as icons.
