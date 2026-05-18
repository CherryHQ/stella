---
name: Stella
description: AI partners for every person
colors:
  ember-gold: "#c27a2a"
  deep-ink: "#1a1a1a"
  constellation-blue: "#4a5f8a"
  soft-cloud: "#f0eff2"
  warm-paper: "#faf9f7"
  muted-steel: "#8b8f96"
  signal-red: "#c44a32"
typography:
  display:
    fontFamily: "Outfit, sans-serif"
    fontSize: "clamp(1.75rem, 4vw, 2.5rem)"
    fontWeight: 600
    lineHeight: 1.15
    letterSpacing: "-0.02em"
  headline:
    fontFamily: "Outfit, sans-serif"
    fontSize: "1.25rem"
    fontWeight: 600
    lineHeight: 1.3
    letterSpacing: "-0.01em"
  title:
    fontFamily: "Outfit, sans-serif"
    fontSize: "1rem"
    fontWeight: 500
    lineHeight: 1.4
  body:
    fontFamily: "Outfit, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.6
  label:
    fontFamily: "Fira Code, monospace"
    fontSize: "0.6875rem"
    fontWeight: 500
    lineHeight: 1.4
    letterSpacing: "0.04em"
rounded:
  sm: "4px"
  md: "8px"
  lg: "12px"
  xl: "16px"
  full: "9999px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "16px"
  lg: "24px"
  xl: "32px"
  section: "48px"
components:
  button-primary:
    backgroundColor: "{colors.ember-gold}"
    textColor: "#ffffff"
    rounded: "{rounded.lg}"
    padding: "10px 20px"
  button-primary-hover:
    backgroundColor: "#a86820"
    textColor: "#ffffff"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.deep-ink}"
    rounded: "{rounded.lg}"
    padding: "10px 16px"
  button-ghost-hover:
    backgroundColor: "{colors.soft-cloud}"
  input-default:
    backgroundColor: "{colors.warm-paper}"
    textColor: "{colors.deep-ink}"
    rounded: "{rounded.lg}"
    padding: "10px 14px"
  chip-active:
    backgroundColor: "{colors.deep-ink}"
    textColor: "#ffffff"
    rounded: "{rounded.full}"
    padding: "4px 12px"
  chip-inactive:
    backgroundColor: "{colors.soft-cloud}"
    textColor: "{colors.muted-steel}"
    rounded: "{rounded.full}"
    padding: "4px 12px"
---

# Design System: Stella

## 1. Overview

**Creative North Star: "The Constellation Desk"**

A smart shared workspace where people, agents, memories, tools, and routines connect naturally. The interface feels like a calm room organized by someone who understands the people inside it: warm materials, clear sight lines, everything reachable without clutter. Not a dashboard. Not a chat window. A place where conversation, delegation, memory, and configuration coexist without competing.

The system rejects messaging-app anxiety (Slack), generic AI-product sameness (ChatGPT's empty center-chat), and enterprise admin-panel energy. It draws instead from Arc Browser's opinionated confidence, iA Writer's typographic clarity, and the warmth of a well-curated personal tool.

**Key Characteristics:**

- Conversation-first: chat is the anchor; panels serve it, never compete
- Progressive disclosure: simple surface, depth on demand
- Warm neutrals with a single golden accent that appears sparingly
- Monospace labels and serif accents create intellectual texture without pretension
- Tactile interactivity: elements respond visibly, transitions acknowledge action

## 2. Colors

A restrained palette anchored by warm neutrals and one golden accent. The gold is rare; its scarcity is the point.

### Primary

- **Ember Gold** (oklch(0.642 0.1691 38.5815)): The singular accent. Used for primary actions, active states, and the occasional punctuation mark. Never floods a surface.

### Secondary

- **Constellation Blue** (oklch(0.414 0.085 259.9)): Supporting role for secondary actions, links, and data visualization accents. Cooler counterweight to the gold.

### Neutral

- **Deep Ink** (oklch(0.321 0 0)): Primary text and high-contrast foreground. Not pure black; carries warmth.
- **Muted Steel** (oklch(0.551 0.023 264.4)): Secondary text, placeholders, disabled states.
- **Soft Cloud** (oklch(0.985 0.002 247.8)): Muted backgrounds, hover states, section separators.
- **Warm Paper** (oklch(0.938 0.004 236.5)): App background. Not white, not grey; faintly blue-tinted warmth.

### Named Rules

**The Ember Rule.** The primary gold appears on no more than 10% of any given screen. One button, one active indicator, one highlighted element. Its rarity creates hierarchy.

**The No Pure Black Rule.** Neither `#000` nor `oklch(0 0 0)` appears anywhere. The darkest value is Deep Ink; the lightest is never `#fff`. Every extreme carries a whisper of warmth.

## 3. Typography

**Display Font:** Outfit (with system-ui fallback)
**Body Font:** Outfit (with system-ui fallback)
**Label/Mono Font:** Fira Code (with monospace fallback)
**Serif Font:** Merriweather (for editorial accents and the home page)

**Character:** Outfit's geometric warmth carries the entire UI: friendly enough for non-technical users, structured enough for information density. Fira Code provides monospace texture for labels, metadata, and code. Merriweather appears only on the home page for editorial weight.

### Hierarchy

- **Display** (600, clamp(1.75rem, 4vw, 2.5rem), line-height 1.15): Page-level titles only. Tight tracking (-0.02em) for density at scale.
- **Headline** (600, 1.25rem, line-height 1.3): Section headers, panel titles. Slightly tighter tracking (-0.01em).
- **Title** (500, 1rem, line-height 1.4): Card headers, list item labels, navigation items.
- **Body** (400, 0.875rem, line-height 1.6): All running text, messages, descriptions. Max line length: 70ch.
- **Label** (Fira Code 500, 0.6875rem, tracking +0.04em, uppercase): Metadata, timestamps, section eyebrows, status indicators.

### Named Rules

**The Two-Family Rule.** At most two font families appear on any single screen: Outfit + Fira Code. Merriweather is home-page-only. Three families on one screen creates visual noise.

## 4. Elevation

The system is predominantly flat. Depth is conveyed through tonal layering (background vs. card vs. popover) rather than shadow theatrics. Shadows appear only as state feedback.

### Shadow Vocabulary

- **Ambient** (`0px 1px 3px 0px hsl(0 0% 10% / 0.05)`): Barely perceptible. Cards at rest, subtle separation.
- **Lifted** (`0px 1px 3px 0px hsl(0 0% 10% / 0.1), 0px 4px 6px -1px hsl(0 0% 10% / 0.1)`): Hover state on interactive cards, floating panels, dropdowns.
- **Elevated** (`0px 1px 3px 0px hsl(0 0% 10% / 0.1), 0px 8px 10px -1px hsl(0 0% 10% / 0.1)`): Modals, command palette, focused popovers.

### Named Rules

**The Flat-By-Default Rule.** Surfaces are flat at rest. Shadows appear only as response to state (hover, focus, active). A shadow that's always visible is a shadow that means nothing.

## 5. Components

### Buttons

- **Shape:** Generously curved (12px radius). Not pill-shaped, not sharp.
- **Primary:** Ember Gold background, white text, 10px 20px padding. Satisfying click: slight scale(0.98) on active.
- **Hover:** Darkens to deeper amber. Transition: 150ms ease-out.
- **Focus:** 2px ring in Ember Gold at 50% opacity, 2px offset.
- **Ghost:** Transparent background, Deep Ink text. Hover reveals Soft Cloud fill. Same radius.
- **Destructive:** Signal Red background, white text. Same shape as Primary.

### Chips / Badges

- **Style:** Full pill radius (9999px). Active: Deep Ink background, white text. Inactive: Soft Cloud background, Muted Steel text.
- **Transition:** Background and color animate together (150ms ease-out). Active state feels like it "fills in."
- **Usage:** Status indicators, filter toggles, scope labels.

### Cards / Containers

- **Corner Style:** Rounded (12px).
- **Background:** Card white (oklch(1 0 0) light / oklch(0.244 0 0) dark).
- **Shadow Strategy:** Flat at rest (ambient shadow only). Lifted on hover for interactive cards.
- **Border:** 1px solid border color. No thick accent borders.
- **Internal Padding:** 16-24px depending on content density.

### Inputs / Fields

- **Style:** Warm Paper background, 1px border, 12px radius. Internal padding 10px 14px.
- **Focus:** Border shifts to Ember Gold. Subtle ring (2px, gold at 30% opacity). No dramatic glow.
- **Placeholder:** Muted Steel, italic optional.

### Navigation / Sidebar

- **Active item:** Soft background tint (sidebar-accent) with Constellation Blue or Ember Gold text for primary indicator.
- **Hover:** Gentle background fill appears (150ms).
- **Section headers:** Label style (Fira Code, uppercase, small, tracked wide). Provides structural rhythm without visual weight.
- **Density:** Compact vertical spacing (6-8px between items). Navigation is scannable, not spacious.

### Conversation Bubble (Signature Component)

- **User messages:** Right-aligned, Soft Cloud background, rounded corners (top-left sharper than others for directional cue).
- **Assistant messages:** Left-aligned, transparent/card background, full-width. Markdown content renders with Body typography.
- **Spacing between messages:** 12-16px. Tight enough to feel conversational, loose enough to scan.

## 6. Do's and Don'ts

### Do:

- **Do** use Ember Gold exclusively for primary actions and active states. Its scarcity creates meaning.
- **Do** let conversation occupy the majority of screen real estate. Panels compress; chat expands.
- **Do** use Fira Code labels as structural anchors (section headers, metadata, timestamps). They provide rhythm.
- **Do** transition interactive elements on hover/focus (150ms ease-out). The interface should feel alive and responsive to touch.
- **Do** use progressive disclosure. Hide configuration behind explicit "Edit" actions; show read-only summaries by default.
- **Do** maintain tonal hierarchy: background < muted < card < popover. Depth through color, not shadow.

### Don't:

- **Don't** create messaging-app anxiety. No unread counts, no notification badges, no channel-list overwhelm. This is Stella, not Slack.
- **Don't** use the centered-empty-chat pattern with floating suggestion chips. That's ChatGPT's generic energy.
- **Don't** use border-left or border-right greater than 1px as colored accent stripes.
- **Don't** apply gradient text (`background-clip: text` + gradient background) anywhere.
- **Don't** use glassmorphism (blur + transparency) decoratively.
- **Don't** create identical card grids (icon + heading + text, repeated). Vary rhythm and density.
- **Don't** reach for a modal as the first thought. Inline editing, slide-over panels, and progressive reveals come first.
- **Don't** use em dashes. Commas, colons, semicolons, or periods.
- **Don't** show admin-panel energy: grey-on-grey data tables, dense configuration forms without context. This is personal, not enterprise.
