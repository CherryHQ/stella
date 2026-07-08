# Stella Web Theme — Teal

**This file is the swappable half of the design system.** It describes the current visual direction only; replace it together with `web/src/tokens.css` when adopting a new style (procedure: [`web-theming.md`](./web-theming.md)). Engineering rules that survive any theme — CossUI contract, layout patterns, token discipline — live in [`web-ui.md`](./web-ui.md) and never change with the theme.

Source: designer "human / approachable" direction (Airbnb / Mercury / Notion), adapted to the shadcn token schema.

## Visual direction

Stella reads as a **friendly, calm, software-native assistant**: clean neutral canvas, one confident teal accent, comfortable radii, and gentle elevation that makes surfaces feel touchable without shouting.

- **Tinted neutrals, not pure gray.** Every neutral — background, surface, border, muted text — carries a faint teal undertone (hue ~200, low chroma). This is what makes the theme read as one piece: the grays look related to the teal accent, not pasted next to it. Never use pure hueless gray (`oklch(L 0 0)`) for surfaces or borders.
- **Light canvas is the native medium.** A calm tinted near-white canvas with **pure white** raised surfaces; the white card lifts off the tinted canvas. Dark mode is a soft teal-tinted near-black, never pure `#000`. No gradient hero, no animated blob, no decorative illustration in the app chrome.
- **Layered, not flat.** Backgrounds stack canvas → surface → elevated with a visible lightness step at each level (≥1.5% L), and overlays carry a soft shadow so menus, popovers, and dialogs lift off the page. A flat white-on-white void leaves the accent with nothing to anchor to.
- **Single chromatic accent.** Teal (`--primary`) carries primary actions, the focus ring, the active tab, and selection. Status colors map to `chart-*`. Everything else stays neutral. Color leads the eye to the one thing that matters per view.
- **Calm motion.** Transitions exist to soften state changes, not to perform. Short, ease-out, no bounce or overshoot.
- **Dense & functional.** Short paragraphs, lists for options, 55–70 char body line length. Wall-of-text answers are a bug. No hero sections or marketing layouts in the app shell.

## Color

Token values live in `src/tokens.css` (`:root` light / `.dark` dark). These tables document the intent behind each value.

### Light surface (brand-defining)

| Semantic role           | Tailwind class                | oklch                    |
| ----------------------- | ----------------------------- | ------------------------ |
| Page canvas             | `bg-background`               | `oklch(0.975 0.008 200)` |
| Card / raised surface   | `bg-card`                     | `oklch(1 0 0)`           |
| Tooltip, popover, hover | `bg-popover`                  | `oklch(1 0 0)` + shadow  |
| Subtle chip / button    | `bg-secondary` / `bg-muted`   | `oklch(0.95 0.012 198)`  |
| Divider, input border   | `border-border`               | `oklch(0.9 0.012 200)`   |
| Body copy, headings     | `text-foreground`             | `oklch(0.2 0.022 220)`   |
| Meta, captions, labels  | `text-muted-foreground`       | `oklch(0.48 0.025 205)`  |
| Primary CTA, focus ring | `bg-primary` / `text-primary` | `oklch(0.55 0.14 172)`   |
| Accent tint background  | `bg-accent`                   | `oklch(0.93 0.05 178)`   |
| Accent tint text        | `text-accent-foreground`      | `oklch(0.43 0.11 178)`   |
| Recessed sidebar panel  | `bg-sidebar`                  | `oklch(0.965 0.01 198)`  |
| Error state             | `bg-destructive`              | `oklch(0.58 0.2 25)`     |

### Dark surface

| Semantic role   | Tailwind class    | Value                   |
| --------------- | ----------------- | ----------------------- |
| Page background | `bg-background`   | `oklch(0.18 0.014 215)` |
| Card surface    | `bg-card`         | `oklch(0.21 0.016 215)` |
| Elevated        | `bg-popover`      | `oklch(0.24 0.018 215)` |
| Border          | `border-border`   | `oklch(0.28 0.016 210)` |
| Body copy       | `text-foreground` | `oklch(0.95 0.006 200)` |
| Accent (teal)   | `bg-primary`      | `oklch(0.7 0.14 174)`   |

### Status colors

Map semantic status to the `chart-*` tokens — never invent one-off color aliases.

| Status         | Token         | oklch (light)          |
| -------------- | ------------- | ---------------------- |
| Info / running | `chart-2`     | `oklch(0.6 0.13 230)`  |
| Success        | `chart-3`     | `oklch(0.68 0.15 150)` |
| Warning        | `chart-4`     | `oklch(0.74 0.15 78)`  |
| Error          | `destructive` | `oklch(0.58 0.2 25)`   |

### Accent usage rules

- Accent (teal) leads to the primary action, active state, and focus per view — keep it from being decorative wallpaper.
- White text on teal is for buttons and badges only — never for body copy.
- `bg-accent` (the subtle teal tint) is allowed for selected rows, active nav items, and hover states. Don't flood whole containers with full-strength teal.

## Typography

### Families

| Role                    | Family         | Tailwind              |
| ----------------------- | -------------- | --------------------- |
| UI / body / display     | Inter          | `font-sans` (default) |
| Code / citations        | JetBrains Mono | `font-mono`           |
| Brand wordmark "stella" | Inter          | `font-serif italic`   |

Inter is loaded from Google Fonts (`@import` in `src/tokens.css`). Do not substitute with Helvetica or Arial — weight rendering differs.

### Scale

| Label      | Size                        | Weight        | Usage                            |
| ---------- | --------------------------- | ------------- | -------------------------------- |
| Display    | `text-2xl` – `text-4xl`     | 600 (not 700) | Page title, hero answer headline |
| Heading L  | `text-xl`                   | 600           | Section heading                  |
| Heading M  | `text-lg`                   | 600           | Sub-section heading              |
| Body       | `text-sm` (15px equivalent) | 400           | Primary reading copy, UI labels  |
| Body small | `text-xs`                   | 400           | Meta, captions, secondary text   |

### Rules

- Default tracking: `-0.01em` (set globally). Don't override unless intentional.
- Use weight 600 for display and headings; reserve 700 for tight, deliberate weight contrast on small display moments. Avoid 700 across long headings.
- Line length: 55–70 characters for body copy. Wider than 80 chars breaks comprehension in dense reading layouts.
- `text-sm` is the baseline for UI labels, table cells, sidebar items — not `text-base`.
- Brand wordmark: `font-serif italic text-xl tracking-tight select-none`.

## Spacing & density

An 8px rhythm on a 4px base — 8px-aligned by default, tighter where dense UI needs it.

| Context               | Spacing                          |
| --------------------- | -------------------------------- |
| Page padding          | `px-4` to `px-6`                 |
| Card internal padding | `p-4` or `p-6`                   |
| Between stacked items | `gap-2` (8px) or `gap-3` (12px)  |
| Between sections      | `gap-6` (24px) or `gap-8` (32px) |
| Inline element gaps   | `gap-1` (4px) or `gap-2` (8px)   |
| Icon-to-label         | `gap-2` (8px)                    |
| Tight lists (sidebar) | `gap-0.5` (2px) or `gap-1` (4px) |

- Max content width: ~720px for reading columns (answer/chat), ~1100px for the full-page shell.
- Dense UI (tables, lists, sidebars) uses tighter spacing. Content areas get more breathing room.

## Border radius

Comfortable, approachable radii — softer than a pure utility dashboard.

| Token  | Value  | Tailwind       | Usage                          |
| ------ | ------ | -------------- | ------------------------------ |
| Small  | ~5px   | `rounded-sm`   | Inline badge, tag, source chip |
| Medium | ~9px   | `rounded-md`   | Input field, button            |
| Large  | 12px   | `rounded-lg`   | Card, modal, popover, dialog   |
| XL     | ~17px  | `rounded-xl`   | Bottom sheet, floating panel   |
| Full   | 9999px | `rounded-full` | Pill button, avatar            |

- Inputs and buttons: `rounded-md`. Cards: `rounded-lg` / `rounded-2xl` (CossUI default). Avatars: `rounded-full`.
- Never use pill radii on cards — pill shapes are reserved for tags and avatars only.
- Never mix radius sizes within the same visual group.

## Elevation & depth

Gentle elevation. Shadow tokens carry soft, low-opacity values so overlays float; flat surfaces still lean on borders.

- **Three background levels:** base → surface → elevated. Overlays add a soft shadow on top.
- **Overlays float:** menus, popovers, dialogs, and dropdowns use the CossUI default `shadow-*` — now real, soft shadows rather than `none`.
- **Cards stay calm:** content cards use `border-border` 1px; add `shadow-sm` only on interactive/hoverable cards, never as default decoration.
- **Header glass effect:** `bg-card/65 backdrop-blur-xl` for sticky headers and floating toolbars.
- Dark mode keeps shadows subtle and reads elevation mostly through background steps.

## Motion

Calm, quick, ease-out. Motion softens state changes; it never performs.

| Action               | Duration | Easing          |
| -------------------- | -------- | --------------- |
| Hover state change   | 150ms    | `ease-out`      |
| Panel / sidebar open | 180ms    | `ease-out`      |
| Modal appear         | 200ms    | `ease-out`      |
| Skeleton shimmer     | 1400ms   | `ease` infinite |
| Accordion expand     | 200ms    | `ease-in-out`   |

- No bounce, no overshoot. A gentle scale/opacity on overlays is fine; spring physics is not.
- No entrance animations for streamed content — the answer appears via the model stream, not a CSS fade sequence.
- Transition only `opacity`, `transform`, `color`, and `background-color`. Never animate layout properties (`width`, `height`, `margin`).
- Allowed: sidebar/Sheet/Dialog transitions (handled by CossUI), `transition-colors duration-150` on hover, skeleton pulses, spinner rotation.

## Voice & UI copy

**Friendly, clear, direct** — warmer than a terminal, never cutesy.

| Context     | Pattern                                                 |
| ----------- | ------------------------------------------------------- |
| Empty state | Direct, plain statement, no emoji, no exclamation mark  |
| Loading     | `Searching…` — ellipsis, no spinner label               |
| Error       | `Something went wrong. Try again.` — direct, no apology |
| Success     | No toast — the result is the confirmation               |
| CTA         | Verb-only: `Search`, `Ask`, `Send` — no "Click to …"    |

What Stella is not: not playful to a fault (no winking emoji, no slang), not enterprise-formal (no "leverage", no "synergy"), not decorative (no hero illustrations, no abstract art in the UI chrome).

## Anti-patterns (theme)

Banned while this theme is active:

| Don't                              | Why                                                         | Do instead                              |
| ---------------------------------- | ----------------------------------------------------------- | --------------------------------------- |
| Purple / violet accents            | The brand accent is teal; violet belongs to the old theme   | Use `bg-primary` (teal)                 |
| Gradient backgrounds               | Canvas is a flat calm neutral; no hero gradients or blobs   | Flat `bg-background`                    |
| Heavy drop shadows                 | Elevation is soft — low-opacity shadows on overlays only    | Use token `shadow-sm`/`shadow-md`       |
| Full-strength teal container fills | Teal leads one action per view, not whole panels            | `bg-accent` tint for selected/active    |
| Rounded pill cards                 | Pill shapes are reserved for tags and avatars only          | `rounded-lg` or `rounded-2xl` for cards |
| Decorative illustration            | No isometric 3D, no blob characters in UI chrome            | If showing an image, it must be content |
| Accent overuse                     | Teal appears for the primary action / active state per view | Keep accent surgical                    |
| Entrance animations on content     | Content appears immediately                                 | Only transition state changes           |
| Hero sections / marketing layouts  | App shell is functional and dense                           | Keep app-like density                   |
| ALL CAPS or Title Case labels      | Sentence case throughout                                    | Sentence case for section labels        |
