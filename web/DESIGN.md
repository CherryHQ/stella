# Stella Web Design System

Agent constraint document. Follow these rules when generating, editing, or reviewing UI code in `web/`.

Based on the **Perplexity AI** design system. Reference: `.claude/skills/designer/references/design-systems/perplexity/DESIGN.md`.

## Visual direction

Stella adapts Perplexity's research-terminal aesthetic: **grounded, sharp, credible, quiet**.

- **Dark canvas is the native medium.** The flat near-black background recedes so content leads. No gradient hero, no animated blob, no decorative illustration. The chrome disappears and the content leads.
- **Three-level depth.** Backgrounds stack in exactly three levels: base → surface → elevated. Do not invent a fourth. Elevation is communicated through background color steps and 1px borders — never box-shadow.
- **Single chromatic accent.** Perplexity Purple (`--primary` / violet) is reserved for a single interactive element per view — the primary action, focus ring, or active tab. Everything else is grayscale. Color appears once per view, never twice.
- **Anti-animation.** Every transition exists to prevent jarring state jumps, not to delight. No spring physics, no bounce, no overshoot.
- **Dense & functional.** Short paragraphs, lists for options, 55–70 char body line length. Wall-of-text answers are a bug. No hero sections or marketing layouts in the app shell.

## Color system

Colors are oklch CSS custom properties in `src/globals.css`, managed by a tweakcn shadcn preset. Never edit token values manually.

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

### stella-os semantic layer

For app-level surfaces beyond shadcn's vocabulary:

| Token                                        | Role                         |
| -------------------------------------------- | ---------------------------- |
| `--stella-os-canvas` / `canvas-muted`        | Full-page backgrounds        |
| `--stella-os-surface` / `surface-raised`     | Card and popover backgrounds |
| `--stella-os-ink` / `muted`                  | Primary and secondary text   |
| `--stella-os-rule`                           | Dividers and borders         |
| `--stella-os-accent` / `accent-soft`         | Interactive highlights       |
| `--stella-os-warning` / `success` / `danger` | Status colors                |

Reference via `var(--stella-os-*)` only when no Tailwind utility exists.

### Accent usage rules

- Accent is reserved for a single interactive element per view — the primary action. Do not use it decoratively.
- Never use white text on the accent color in body copy; reserve that combination for buttons and badges only.
- Never apply accent/violet as a background fill for containers or sections.

### Color rules

- Always use semantic Tailwind utilities: `bg-background`, `text-foreground`, `text-primary`, `bg-muted`, `border-border`, etc.
- Never hardcode color values: no `text-[#abc]`, no `bg-[oklch(...)]`, no inline `style={{ color }}`.
- Never reference `var(--some-color)` directly in JSX when a Tailwind utility exists.
- To change the theme globally, run the tweakcn command in `CLAUDE.md` — no component edits needed.

## Typography

### Families

| Role                    | Family         | Tailwind              |
| ----------------------- | -------------- | --------------------- |
| UI / body / display     | Inter          | `font-sans` (default) |
| Code / citations        | JetBrains Mono | `font-mono`           |
| Brand wordmark "stella" | Inter          | `font-serif italic`   |

Inter is loaded from Google Fonts. Do not substitute with Helvetica or Arial — weight rendering differs.

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

## Spacing & grid

### Base unit

4px (`--spacing: 0.25rem`). All spacing uses Tailwind's spacing scale.

Perplexity's design uses an 8px rhythm — the 4px base allows both 8px-aligned and tighter spacing where needed.

### Common patterns

| Context               | Spacing                                                         |
| --------------------- | --------------------------------------------------------------- |
| Page padding          | `px-4` to `px-6`                                                |
| Card internal padding | `p-4` or `p-6` (maps to Perplexity's `--space-4` / `--space-5`) |
| Between stacked items | `gap-2` (8px) or `gap-3` (12px)                                 |
| Between sections      | `gap-6` (24px) or `gap-8` (32px)                                |
| Inline element gaps   | `gap-1` (4px) or `gap-2` (8px)                                  |
| Icon-to-label         | `gap-2` (8px)                                                   |
| Tight lists (sidebar) | `gap-0.5` (2px) or `gap-1` (4px)                                |

### Layout grid

- **Max content width:** ~720px for reading columns (answer/chat), ~1100px for the full-page shell.
- **Sidebar:** 16rem (256px) desktop, 18rem (288px) mobile, 3rem (48px) collapsed icon-only.
- Mobile: single column, 16px side margin.

### Rules

- Never use arbitrary spacing values like `p-[13px]`. Stick to the 4px grid.
- Prefer `gap-*` on flex/grid parents over margin on children.
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

### Rules

- Inputs and buttons: `rounded-md`.
- Cards: `rounded-lg` (CossUI Card uses `rounded-2xl` — acceptable as a Stella adaptation).
- Avatars: `rounded-full`.
- Never use pill radii on cards — pill shapes are reserved for tags and avatars only (Perplexity anti-pattern).
- Never mix radius sizes within the same visual group.

## Elevation & depth

Flat design. All shadow tokens are set to `none`. Elevation is communicated through border contrast and background color steps, not shadow.

- **Three background levels only:** base → surface → elevated. Do not invent a fourth.
- **Header glass effect:** `bg-card/65 backdrop-blur-xl` for sticky headers and floating toolbars — Stella adaptation for the app shell.
- **Border as elevation:** Cards and containers use `border-border` 1px. No `box-shadow`.
- Don't add `shadow-*` classes. They resolve to `none` by design.

## Layout patterns

### App shell

```
┌─ SiteHeader (h-14, border-b, full-width) ──────────────────────┐
│ ┌─ Sidebar ─────┐ ┌─ SidebarInset ───────────────────────────┐ │
│ │ collapsible    │ │ ┌─ Sub-header (h-12, backdrop-blur) ──┐ │ │
│ │ offcanvas      │ │ │  trigger · separator · title · acts │ │ │
│ │ 16rem desktop  │ │ └─────────────────────────────────────┘ │ │
│ │ 18rem mobile   │ │ ┌─ Content (flex-1 overflow-hidden) ─┐ │ │
│ │ 3rem collapsed │ │ │                                     │ │ │
│ │                │ │ │                                     │ │ │
│ └────────────────┘ │ └─────────────────────────────────────┘ │ │
│                    └─────────────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────┘
```

### Resizable panels

For split-pane views (e.g., session + inspector):

- Use CSS `flex` with draggable divider, not CSS Grid.
- Set min-width constraints and auto-hide breakpoints.
- Mobile: fall back to Sheet (bottom or right).

### Responsive rules

- Mobile breakpoint: `max-md` (< 768px).
- Sidebar: offcanvas modal on mobile, persistent on desktop. Collapses under 900px.
- Multi-column layouts: collapse to single-column stacked on mobile.
- Touch targets: CossUI Button handles `@media (pointer: coarse)` padding automatically.

## Components

### CossUI first — zero custom styling

Always use CossUI primitives from `src/components/ui/`. The library has 53+ components built on `@base-ui/react`.

**Hard rules:**

- **Never hand-write a primitive that CossUI covers.** If CossUI has a Button, Dialog, Select, Card, Tabs, etc. — use it. No exceptions.
- **Never override CossUI component styles inline or with extra Tailwind classes that change the component's visual identity** (colors, radius, shadows, padding, font). Use the component's variant/size props instead. If a variant doesn't exist, propose adding it to the CossUI component — don't patch the call site.
- **Never create a one-off styled wrapper** around a CossUI component to "customize" it. If the wrapper only adds Tailwind classes, those classes belong in the CossUI component's variant system or in `globals.css`.
- **Compose, don't customize.** Build complex UI by composing CossUI primitives together, not by restyling individual primitives. A settings form is Field + Input + Select + Button composed — not a custom `<SettingsInput>` with bespoke styles.
- **Feature components inherit, not override.** Components in `src/features/` should use CossUI with default styling. The only Tailwind classes allowed on feature components are layout concerns: `flex`, `grid`, `gap-*`, `p-*`, `w-*`, `h-*`, `overflow-*`. Color, typography, radius, and shadow come from CossUI and the theme — not from the feature.

**Why:** When we change themes (swap tweakcn preset, adjust DESIGN.md direction), every page should update automatically. A component that hardcodes `bg-violet-500` or `rounded-xl` or `text-lg font-bold` on a CossUI primitive breaks this contract and becomes a manual migration target.

**Allowed on feature components:** layout utilities only.

```tsx
{
  /* GOOD — layout only, CossUI handles visual identity */
}
<div className="flex flex-col gap-4 p-6">
  <Card>
    <CardHeader>
      <CardTitle>{t("settings.title")}</CardTitle>
    </CardHeader>
    <CardPanel>
      <Field label={t("settings.name")}>
        <Input value={name} onChange={setName} />
      </Field>
    </CardPanel>
    <CardFooter>
      <Button>{t("common.save")}</Button>
    </CardFooter>
  </Card>
</div>;

{
  /* BAD — visual overrides on CossUI components */
}
<Card className="bg-violet-50 rounded-3xl shadow-lg border-2 border-violet-200">
  <Button className="bg-gradient-to-r from-purple-500 to-pink-500 text-white rounded-full px-8">
    Save
  </Button>
</Card>;
```

Read `.agents/skills/coss/SKILL.md` for imports, composition rules, and particle examples.

### Button variants

| Variant                       | Usage                                                    |
| ----------------------------- | -------------------------------------------------------- |
| `default`                     | Primary action — one per view (accent color)             |
| `secondary`                   | Standard actions                                         |
| `ghost`                       | Toolbar / sidebar actions — transparent, border on hover |
| `outline`                     | Secondary emphasis                                       |
| `destructive`                 | Delete, remove, irreversible actions                     |
| `link`                        | Inline navigation                                        |
| `premium` / `premium-outline` | Upgrade / paid features                                  |

### Card composition

```tsx
<Card>
  <CardHeader>
    <CardTitle>Title</CardTitle>
    <CardDescription>Subtitle</CardDescription>
    <CardAction>
      <Button variant="ghost" size="icon-sm">
        ...
      </Button>
    </CardAction>
  </CardHeader>
  <CardPanel>...</CardPanel>
  <CardFooter>...</CardFooter>
</Card>
```

### Form patterns

- Wrap inputs in `<Field>` for label + description + error.
- Group related fields with `<Fieldset>`.
- Use CossUI form primitives: Input, Select, Combobox, Checkbox, Switch, Radio, NumberField.
- Validation errors display via Field's built-in error slot.

### Overlay decision tree

| Need                                     | Component                    | Example                          |
| ---------------------------------------- | ---------------------------- | -------------------------------- |
| Blocking confirmation or multi-step form | **Dialog** (`DialogPopup`)   | Create group, confirm delete     |
| Side panel with detail/inspector content | **Sheet** (`SheetPopup`)     | Mobile inspector, session detail |
| Small contextual options from a trigger  | **Popover** (`PopoverPopup`) | Color picker, quick settings     |
| Action menu from a trigger               | **Menu** (`DropdownMenu`)    | Row actions, user menu           |

Rules:

- On mobile, replace side panels with Sheet (bottom or right), not Dialog.
- Prefer inline resolution (inline pickers, inline follow-ups) over spawning a blocking Dialog.
- Never nest overlays — a Dialog inside a Sheet, or a Popover inside a Dialog, is a bug.

### Toast notifications

Use the custom `useToast()` hook from `@/hooks/use-toast`, not a shadcn toast.

```tsx
const { showToast } = useToast();
showToast("Saved successfully", "success");
showToast(error.message, "error");
```

Kinds: `"success"` | `"error"`. Default duration: 3000ms. Toasts render at `z-[9999]`.

### Loading & empty states

- **Loading:** Use TanStack Query's `isLoading` from `useQuery`. Display a Spinner or brief text ("Loading…"), never a full-page skeleton unless the page is data-heavy.
- **Empty state:** Use the shared `SettingsEmptyState` component (`@/features/settings/SettingsEmptyState`) with props `{ icon?, message, description?, action? }`. Don't hand-write empty states.
- **Error:** Display inline error text. Direct tone: "Something went wrong. Try again." — no apology.

### Form validation

No form library (no Zod, no React Hook Form). Use plain `useState` per field with inline validation logic.

```tsx
const [name, setName] = useState("");
const canSubmit = name.trim().length > 0;
```

Display errors inline via Field's error slot or adjacent text. Don't invent a validation framework.

### Z-index layers

| Layer                    | Value             | Usage              |
| ------------------------ | ----------------- | ------------------ |
| Base content             | auto              | Normal flow        |
| Sticky headers           | `z-30`            | SiteHeader         |
| Resizable dividers       | `z-10`            | Panel drag handles |
| Overlays (Dialog, Sheet) | Managed by CossUI | Don't override     |
| Toasts                   | `z-[9999]`        | Toast container    |

Don't invent arbitrary z-index values. If a new layer is needed, add it to this table.

## Icons

- Library: `lucide-react`.
- Sizes: 16px and 20px only. Stroke-based, consistent weight.
- In buttons with text: `size={16}` with `gap-2`.
- Standalone icon buttons: use `size="icon-sm"` or `size="icon"` button variant.
- All UI icons are monochrome at `text-muted-foreground`. No multi-color icon sets.
- Never use emoji as icons in the UI.

## Motion

Perplexity's interactions are nearly imperceptible — the brand is anti-animation.

### Timing

| Action               | Duration | Easing          |
| -------------------- | -------- | --------------- |
| Hover state change   | 120ms    | `ease`          |
| Panel / sidebar open | 160ms    | `ease-out`      |
| Modal appear         | 200ms    | `ease-out`      |
| Skeleton shimmer     | 1400ms   | `ease` infinite |
| Accordion expand     | 180ms    | `ease-in-out`   |

### Rules

- No spring physics, no bounce, no overshoot.
- `prefers-reduced-motion: reduce` → all transitions collapse to instant. CossUI handles this for its primitives.
- No entrance animations for content — the answer appears immediately, not with a fade-in sequence. Streaming text appears via model stream, not CSS animation.
- Transition only `opacity`, `transform`, `color`, and `background-color`. Never animate layout properties (`width`, `height`, `margin`).

### Allowed motion

- Sidebar collapse/expand transitions (handled by CossUI Sidebar).
- Sheet/Dialog enter/exit (handled by CossUI primitives).
- Hover state transitions: `transition-colors duration-150`.
- Loading states: skeleton pulses, spinner rotation.

## Dark mode

Theme is class-based: `.dark` on `<html>`. Managed by `src/lib/theme.ts`. Dark mode is the brand-defining surface.

### Rules

- Never use `dark:` variant to override semantic tokens — they switch automatically.
- Use `dark:` only for non-token properties (e.g., `dark:opacity-80` on images).
- Test both modes. A component that looks correct in light mode but broken in dark mode is a bug.
- Scrollbar styling uses `color-mix(in oklch, var(--muted-foreground))` — inherits from theme.

## Voice & UI copy

Adapted from Perplexity's tone: **precise, cited, neutral, dense**.

| Context     | Pattern                                                 |
| ----------- | ------------------------------------------------------- |
| Empty state | Direct statement, no emoji, no exclamation mark         |
| Loading     | `Searching…` — ellipsis, no spinner label               |
| Error       | `Something went wrong. Try again.` — direct, no apology |
| Success     | No toast — the result is the confirmation               |
| CTA         | Verb-only: `Search`, `Ask`, `Send` — no "Click to …"    |

### What Stella is not

- Not playful (no winking emoji, no casual slang in UI copy)
- Not enterprise-formal (no "leverage", no "synergy")
- Not decorative (no hero illustrations, no abstract art in the UI chrome)

## Anti-patterns

These patterns are inconsistent with the Perplexity visual language and are banned:

| Don't                                                  | Why                                                                      | Do instead                                            |
| ------------------------------------------------------ | ------------------------------------------------------------------------ | ----------------------------------------------------- |
| Gradient backgrounds                                   | bg-base is a flat near-black, no hero gradients, no mesh, no color blobs | Flat `bg-background`                                  |
| Drop shadows                                           | Elevation = background color steps + border, never box-shadow            | Use `border-border`                                   |
| Colorful icons                                         | All UI icons are monochrome                                              | `text-muted-foreground` on all icons                  |
| Rounded pill cards                                     | Pill shapes are reserved for tags and avatars only                       | `rounded-lg` or `rounded-2xl` for cards               |
| Decorative illustration                                | No isometric 3D, no blob characters in UI chrome                         | If showing an image, it must be content               |
| Accent overuse                                         | Purple appears once per view: primary button or active focus             | Keep accent surgical                                  |
| Hardcode colors                                        | `#fff`, `oklch(...)`, `rgb()`                                            | Use semantic tokens                                   |
| Arbitrary Tailwind values                              | `w-[347px]`, `p-[13px]`                                                  | Use scale values                                      |
| Multiple icon libraries                                | Consistency requires one library                                         | `lucide-react` only                                   |
| Custom primitives CossUI covers                        | Duplication and inconsistency                                            | Use CossUI; extend with composition                   |
| Override CossUI visual styles at call site             | Breaks theme portability — manual migration on theme change              | Use variant/size props; propose new variant if needed |
| One-off styled wrappers around CossUI                  | Creates shadow component library that won't track theme changes          | Compose CossUI primitives directly                    |
| Color/radius/shadow/font classes on feature components | Feature components should only use layout classes                        | Let CossUI and the theme handle visual identity       |
| Entrance animations                                    | Content appears immediately                                              | Only transition state changes                         |
| Hero sections / marketing layouts                      | App shell is functional and dense                                        | Keep app-like density                                 |
| ALL CAPS or Title Case labels                          | Perplexity uses sentence case                                            | Sentence case for section labels                      |
| `useState` for URL state                               | URL is the single source of truth                                        | Use URL params (see CLAUDE.md)                        |
| Inter at 700 weight for display                        | Reads too thin at large sizes                                            | Use weight 600 for display headings                   |

## Changing the theme

To switch to a different visual direction in the future:

1. **Colors:** Pick a new tweakcn preset and run the shadcn command. This overwrites `globals.css` token values. `--stella-os-*` aliases follow automatically.

   Current preset:

   ```bash
   pnpm dlx shadcn@latest add https://tweakcn.com/r/themes/cmlk6zefr000004lbe9jygsqc
   ```

2. **Visual direction:** Update the "Visual direction" section of this file.
3. **Elevation:** If the new style uses shadows, update the `--shadow-*` values in `globals.css` and revise "Elevation & depth".
4. **Radius:** Adjust `--radius` base in `globals.css` and update the radius table.
5. **Motion:** Revise the motion section if the new direction is more or less animated.
6. **Typography:** Update font imports in `globals.css` and the typography section.
7. **Anti-patterns:** Review and revise the anti-patterns table for the new direction.

No component code changes are needed for token-level changes — the semantic Tailwind layer handles the mapping.
