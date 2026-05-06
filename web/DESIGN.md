## Overview

Anna is a quiet, editorial admin interface for an AI agent platform. The base canvas is **warm cream** (`{colors.base-100}` — #faf9f7) holding warm near-black ink (`{colors.base-content}` — #252320). The single brand voltage is **terra cotta** (`{colors.primary}` — #b8593a) — a burnt-clay tone used for the wordmark, active nav links, and primary CTAs.

Type runs **DM Sans** as the UI family and **DM Serif Text** as the editorial display family. Page titles and the logo use the serif in italic — a deliberate editorial contrast against the sans UI body. **JetBrains Mono** carries every code surface: IDs, API keys, form labels for technical fields, and the footer wordmark.

**Key characteristics:**
- Warm cream canvas, not white. Ink is warm (#252320), not pure black.
- Single primary color: terra cotta (#b8593a). Used for the wordmark, active nav, and primary buttons.
- Display (page titles) uses DM Serif italic at weight 400. Never bold.
- Muted warm-gray (`{colors.secondary}` — #908981) for secondary text, inactive nav, and meta labels.
- Hairline-only depth: `border-base-300` dividers, `shadow-xl` only on modal overlays.
- Consistent 6rem max-content-width with 1.5rem side padding.
- daisyUI 5 semantic tokens throughout — never raw hex in templates.

## Colors

### Brand
- **Primary** (`{colors.primary}` — #b8593a): Terra cotta. Wordmark, active nav link, primary buttons, toggles.
- **Primary Content** (`{colors.primary-content}` — #fdf5f2): Text on primary backgrounds.
- **Accent** (`{colors.accent}` — #e28a6a): Warm peach. Secondary accent, complementary highlights.
- **Accent Content** (`{colors.accent-content}` — #3a3733): Text on accent backgrounds.

### Surface
- **Base 100** (`{colors.base-100}` — #faf9f7): Warm cream. Page floor, card backgrounds.
- **Base 200** (`{colors.base-200}` — #f5f3ef): Hover backgrounds, avatar fills, alternate rows.
- **Base 300** (`{colors.base-300}` — #e8e4dd): Hairline dividers, input borders, card outlines.
- **Base Content** (`{colors.base-content}` — #252320): Default text. Warm near-black.

### Text
- **Secondary** (`{colors.secondary}` — #908981): Warm gray. Subtitles, inactive nav, meta labels, muted captions.
- **Neutral** (`{colors.neutral}` — #54504a): Stronger muted text. Background-inverted surfaces.
- **Neutral Content** (`{colors.neutral-content}` — #f5f3ef): Text on neutral backgrounds.

### Semantic
- **Info** (`{colors.info}` — #3b82f6): Informational states.
- **Success** (`{colors.success}` — #22c55e): Confirmation, enabled state.
- **Warning** (`{colors.warning}` — #f59e0b): Cautionary states.
- **Error** (`{colors.error}` — #ef4444): Validation errors, destructive actions.

## Typography

### Font Families
- **DM Sans** — UI body and labels. Variable weight 300–700, optical size 9–40. Fallback: `system-ui, sans-serif`.
- **DM Serif Text** — Page display headings and brand wordmark (italic). Fallback: `Georgia, serif`.
- **JetBrains Mono** — All monospace surfaces: IDs, keys, code, form labels, and the footer mark. Weights 400 / 500 / 600.

### Type Scale

| Role | Family | Size | Weight | Tracking | Use |
|---|---|---|---|---|---|
| Page title | DM Serif | 3xl / 4xl (md) | 400 | `tracking-tight` | `PageHeader` h1 |
| Logo / wordmark | DM Serif italic | xl | 400 | `tracking-tight` | Navbar brand |
| Section heading | DM Sans | lg | 600 | 0 | Drawer section heads |
| UI body | DM Sans | sm | 400–500 | 0 | Nav links, card body |
| Form label | JetBrains Mono | sm | 500 | 0 | `FormField` labels |
| Meta / caption | DM Sans | xs | 400–500 | 0 | Subtitles, badge text |
| Tiny mono | JetBrains Mono | [11px] | 400 | 0 | IDs, API refs, footer mark |

### Principles
- Page title weight is 400 (DM Serif). Never bold display.
- Monospace for anything technical: IDs, sources, version strings, API keys.
- Secondary text uses `{colors.secondary}` — never reduced opacity on base-content.

## Layout & Spacing

### Base Unit
4px. All spacing in multiples.

### Content Container
- Max width: `max-w-6xl` (72rem / 1152px). Applies to navbar, main, and footer.
- Side padding: `px-6` (1.5rem).
- Main vertical padding: `py-12` (3rem top + bottom).
- Navbar height: `h-14` (3.5rem).

### Section Rhythm
- Page header bottom margin: `mb-8` (2rem).
- Card grid gap: `gap-4` or `gap-6`.
- Form field stacking: `gap-4`.

### Responsive
- Navbar: desktop shows horizontal text nav; mobile falls back to a `<select>` dropdown.
- Max content width stays 72rem at all sizes — only side padding collapses on small screens.

## Elevation & Depth

Anna uses **hairline-only depth**. No elevation tiers. Surface separation comes from 1px borders and the warm white-on-cream card contrast.

| Level | Treatment | Use |
|---|---|---|
| Page floor | `{colors.base-100}` | Body, navbar, footer |
| Card | `{colors.base-100}` + 1px `{colors.base-300}` border | Content cards |
| Hover row | `{colors.base-200}` bg | List item hover |
| Drawer / overlay | `shadow-2xl` + `border-l border-base-300` | Side drawer panels |
| Modal | `shadow-xl` on card inside `bg-black/40` scrim | Confirm dialogs |
| Toast | `shadow-lg` | Top-center notification |

Drop shadows appear only on modal overlays and toasts — never on inline page cards.

## Shapes

### Border Radius

| Token | Value | Use |
|---|---|---|
| `{rounded.sm}` | daisyUI default (`rounded-btn`) | Buttons |
| `{rounded.md}` | `rounded-md` (6px) | Inputs, selects, small pills |
| `{rounded.lg}` | `rounded-lg` (8px) | Compact cards, panels |
| `{rounded.box}` | `rounded-box` (daisyUI) | Dropdown menus, modal cards |
| `{rounded.full}` | `rounded-full` | Avatar initials circle |

Use daisyUI's `rounded-box` for dropdown and modal cards — it inherits from the active theme. Don't hardcode pixel values.

## Components

### Layout Shell

**`Layout`** — Full-page wrapper. `bg-base-100 text-base-content min-h-screen font-sans antialiased flex flex-col`. Contains Navbar, Toast overlay, main content area (`max-w-6xl mx-auto px-6 py-12 flex-1`), and footer.

**`LoginLayout`** — Minimal shell for unauthenticated pages. Same CSS baseline, no Navbar.

### Navbar

**`Navbar`** — `border-b border-base-300`, height `h-14`. Left: serif italic wordmark in `{colors.primary}`. Center: horizontal text nav (desktop) with active link in `{colors.primary}` and inactive in `{colors.secondary}`. Right: avatar dropdown with theme switcher and log out. Mobile: collapses nav to a `select` dropdown.

### Page Header

**`PageHeader`** — `mb-8` block. H1 in DM Serif `text-3xl md:text-4xl tracking-tight`. Optional subtitle in `text-secondary text-sm`.

### Buttons

**`btn-primary`** — Terra cotta fill. daisyUI `btn btn-primary`. Primary action.

**`btn-ghost`** — Transparent, base-content text. `btn btn-ghost`. Secondary / cancel.

**`btn-ghost text-error`** — Destructive ghost. Red text on transparent. Delete actions.

**Sizes:** `btn-sm` (default in drawers), `btn-xs` (inline row actions).

### Inputs

**`input-bordered`** — `input input-bordered` with optional size modifier `input-sm` / `input-xs`. JetBrains Mono on any technical value input.

**`select-bordered`** — `select select-bordered`. Model selectors, enum fields.

**`textarea-bordered`** — `textarea textarea-bordered`. Multi-line content (YAML, markdown, code).

**`toggle-primary`** — `toggle toggle-primary toggle-sm`. Enable/disable boolean field.

### FormField

**`FormField`** — `form-control w-full` wrapper. Label in JetBrains Mono `text-sm font-medium`. Children slot holds the input.

### Cards

**`card`** — `card bg-base-100 shadow-xl`. Used for modal dialogs. Padding 24–32px inside.

Inline page cards (list rows, feature panels) use `border border-base-300 rounded-lg` without shadow — hairline card style.

### Badges

**`Badge`** — daisyUI `badge` with semantic variant: `badge-primary`, `badge-success`, `badge-warning`, `badge-error`, `badge-ghost`. Size: default or `badge-xs` for inline metadata.

### Empty State

**`EmptyState`** — `text-center py-16`. Single `text-secondary text-sm` message.

### Toast

**`Toast`** — Fixed `top-2 left-1/2 -translate-x-1/2 z-50`. daisyUI `alert` in `alert-success` (green) or `alert-error` (red). Text in JetBrains Mono `text-sm font-mono`. Triggered via `this.$store.toast.show(msg)` or `this.$store.toast.show(msg, 'error')`. Auto-dismisses.

### Skills Drawer

**`SkillsDrawer`** — Fixed right-side panel: `fixed top-0 right-0 z-50 h-screen w-full max-w-4xl bg-base-100 shadow-2xl border-l border-base-300`. Two-column: list pane (left, scrollable) + detail pane (right). Backdrop scrim: `fixed inset-0 z-40 bg-black/40`.

## Do's and Don'ts

### Do
- Use daisyUI semantic color tokens (`btn-primary`, `text-secondary`, `border-base-300`) — never hardcode hex in templates.
- Use JetBrains Mono for all technical surfaces: IDs, API keys, source strings, version numbers, form labels for technical fields.
- Use DM Serif Text for page titles (`PageHeader`) and the brand wordmark in `Navbar`.
- Keep display heading weight at 400. No bold page titles.
- Apply `shadow-2xl` only on drawer panels and `shadow-xl` only on modal cards. Inline page cards use hairline borders.
- Use `{colors.secondary}` (`text-secondary`) for all muted / meta text — not opacity on base-content.

### Don't
- Don't introduce a second brand color. Terra cotta (`primary`) is the only one.
- Don't use `shadow-md` or `shadow-lg` on inline page cards. Hairline borders only.
- Don't use display weight 700 (bold) on any DM Serif heading.
- Don't hardcode `rounded-*` values on modals and dropdowns — use daisyUI's `rounded-box`.
- Don't use semantic colors (success, error, warning, info) as decorative accents — only for their semantic meaning.
- Don't add raw inline styles. All styling through Tailwind 4 + daisyUI 5 utilities.
- Don't edit `*_templ.go` files — they are auto-generated. Edit `.templ` sources and run `mise run generate`.
