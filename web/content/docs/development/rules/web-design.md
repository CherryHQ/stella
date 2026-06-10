# Stella Web Theme — Perplexity

**This file is the swappable half of the design system.** It describes the current visual direction only; replace it together with `web/src/tokens.css` when adopting a new style (procedure: [`web-theming.md`](./web-theming.md)). Engineering rules that survive any theme — CossUI contract, layout patterns, token discipline — live in [`web-ui.md`](./web-ui.md) and never change with the theme.

Source: designer design-system package `perplexity`, adapted to the shadcn token schema.

## Visual direction

Stella adapts Perplexity's research-terminal aesthetic: **grounded, sharp, credible, quiet**.

- **Dark canvas is the native medium.** The flat near-black background recedes so content leads. No gradient hero, no animated blob, no decorative illustration. The chrome disappears and the content leads.
- **Three-level depth.** Backgrounds stack in exactly three levels: base → surface → elevated. Do not invent a fourth. Elevation is communicated through background color steps and 1px borders — never box-shadow.
- **Single chromatic accent.** Perplexity Purple (`--primary` / violet) is reserved for a single interactive element per view — the primary action, focus ring, or active tab. Everything else is grayscale. Color appears once per view, never twice.
- **Anti-animation.** Every transition exists to prevent jarring state jumps, not to delight. No spring physics, no bounce, no overshoot.
- **Dense & functional.** Short paragraphs, lists for options, 55–70 char body line length. Wall-of-text answers are a bug. No hero sections or marketing layouts in the app shell.

## Color

Token values live in `src/tokens.css` (`:root` light / `.dark` dark). These tables document the intent behind each value.

### Dark surface (brand-defining)

| Semantic role           | Tailwind class                | Perplexity equivalent      | oklch                   |
| ----------------------- | ----------------------------- | -------------------------- | ----------------------- |
| Page background         | `bg-background`               | `--bg-base` #0f0f10        | `oklch(0.12 0.004 280)` |
| Card / sidebar surface  | `bg-card`                     | `--bg-surface` #19191a     | `oklch(0.16 0.004 280)` |
| Tooltip, popover, hover | `bg-popover`                  | `--bg-elevated` #232325    | `oklch(0.2 0.005 280)`  |
| Divider, input border   | `border-border`               | `--border` #2e2e30         | `oklch(0.25 0.005 280)` |
| Body copy, headings     | `text-foreground`             | `--text-primary` #f0f0f0   | `oklch(0.95 0 0)`       |
| Meta, captions, labels  | `text-muted-foreground`       | `--text-secondary` #9b9b9b | `oklch(0.65 0 0)`       |
| Primary CTA, focus ring | `bg-primary` / `text-primary` | `--accent` #a855f7         | `oklch(0.62 0.22 307)`  |
| Accent hover            | `text-accent-foreground`      | `--accent-hover` #c084fc   | `oklch(0.72 0.2 307)`   |
| Accent tint background  | `bg-accent`                   | `--accent-subtle` #3b1f5e  | `oklch(0.22 0.14 307)`  |
| Error state             | `bg-destructive`              | `--error` #ef4444          | `oklch(0.63 0.21 27)`   |

### Light surface

| Semantic role   | Tailwind class    | Value                           |
| --------------- | ----------------- | ------------------------------- |
| Page background | `bg-background`   | `oklch(1 0 0)` white            |
| Card surface    | `bg-card`         | `oklch(0.98 0 0)` #f8f8f8       |
| Elevated        | `bg-popover`      | `oklch(0.95 0 0)` #f0f0f0       |
| Border          | `border-border`   | `oklch(0.9 0 0)` #e0e0e0        |
| Body copy       | `text-foreground` | `oklch(0.12 0.004 280)` #0f0f10 |
| Accent          | `bg-primary`      | `oklch(0.51 0.23 293)` #7c3aed  |

### Accent usage rules

- Accent is reserved for a single interactive element per view — the primary action. Do not use it decoratively.
- Never use white text on the accent color in body copy; reserve that combination for buttons and badges only.
- Never apply accent/violet as a background fill for containers or sections.

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
- Bold (700) is avoided — use 600 for headings. 700 reads too thin against dark backgrounds at display sizes.
- Line length: 55–70 characters for body copy. Wider than 80 chars breaks comprehension in dense reading layouts.
- `text-sm` is the baseline for UI labels, table cells, sidebar items — not `text-base`.
- Brand wordmark: `font-serif italic text-xl tracking-tight select-none`.

## Spacing & density

Perplexity's design uses an 8px rhythm — the 4px base unit allows both 8px-aligned and tighter spacing where needed.

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

Perplexity uses compact, functional radii:

| Token  | Value  | Tailwind       | Usage                          |
| ------ | ------ | -------------- | ------------------------------ |
| Small  | 4px    | `rounded-sm`   | Inline badge, tag, source chip |
| Medium | 8px    | `rounded-md`   | Input field, button, card      |
| Large  | 12px   | `rounded-lg`   | Modal, popover, dialog         |
| XL     | 16px   | `rounded-xl`   | Bottom sheet, floating panel   |
| Full   | 9999px | `rounded-full` | Pill button, avatar            |

- Inputs and buttons: `rounded-md`. Cards: `rounded-lg` (CossUI Card uses `rounded-2xl` — acceptable as a Stella adaptation). Avatars: `rounded-full`.
- Never use pill radii on cards — pill shapes are reserved for tags and avatars only.
- Never mix radius sizes within the same visual group.

## Elevation & depth

Flat design. All shadow tokens are set to `none`. Elevation is communicated through border contrast and background color steps, not shadow.

- **Three background levels only:** base → surface → elevated. Do not invent a fourth.
- **Header glass effect:** `bg-card/65 backdrop-blur-xl` for sticky headers and floating toolbars — Stella adaptation for the app shell.
- **Border as elevation:** Cards and containers use `border-border` 1px. No `box-shadow`.
- Don't add `shadow-*` classes. They resolve to `none` by design.

## Motion

Perplexity's interactions are nearly imperceptible — the brand is anti-animation.

| Action               | Duration | Easing          |
| -------------------- | -------- | --------------- |
| Hover state change   | 120ms    | `ease`          |
| Panel / sidebar open | 160ms    | `ease-out`      |
| Modal appear         | 200ms    | `ease-out`      |
| Skeleton shimmer     | 1400ms   | `ease` infinite |
| Accordion expand     | 180ms    | `ease-in-out`   |

- No spring physics, no bounce, no overshoot.
- No entrance animations for content — the answer appears immediately, not with a fade-in sequence. Streaming text appears via model stream, not CSS animation.
- Transition only `opacity`, `transform`, `color`, and `background-color`. Never animate layout properties (`width`, `height`, `margin`).
- Allowed: sidebar/Sheet/Dialog transitions (handled by CossUI), `transition-colors duration-150` on hover, skeleton pulses, spinner rotation.

## Voice & UI copy

Adapted from Perplexity's tone: **precise, cited, neutral, dense**.

| Context     | Pattern                                                 |
| ----------- | ------------------------------------------------------- |
| Empty state | Direct statement, no emoji, no exclamation mark         |
| Loading     | `Searching…` — ellipsis, no spinner label               |
| Error       | `Something went wrong. Try again.` — direct, no apology |
| Success     | No toast — the result is the confirmation               |
| CTA         | Verb-only: `Search`, `Ask`, `Send` — no "Click to …"    |

What Stella is not: not playful (no winking emoji, no casual slang), not enterprise-formal (no "leverage", no "synergy"), not decorative (no hero illustrations, no abstract art in the UI chrome).

## Anti-patterns (theme)

These patterns are inconsistent with the Perplexity visual language and are banned while this theme is active:

| Don't                             | Why                                                           | Do instead                              |
| --------------------------------- | ------------------------------------------------------------- | --------------------------------------- |
| Gradient backgrounds              | bg-base is flat near-black; no hero gradients, no color blobs | Flat `bg-background`                    |
| Drop shadows                      | Elevation = background color steps + border, never box-shadow | Use `border-border`                     |
| Rounded pill cards                | Pill shapes are reserved for tags and avatars only            | `rounded-lg` or `rounded-2xl` for cards |
| Decorative illustration           | No isometric 3D, no blob characters in UI chrome              | If showing an image, it must be content |
| Accent overuse                    | Purple appears once per view: primary button or active focus  | Keep accent surgical                    |
| Entrance animations               | Content appears immediately                                   | Only transition state changes           |
| Hero sections / marketing layouts | App shell is functional and dense                             | Keep app-like density                   |
| ALL CAPS or Title Case labels     | Perplexity uses sentence case                                 | Sentence case for section labels        |
| Inter at 700 weight for display   | Reads too thin at large sizes                                 | Use weight 600 for display headings     |
