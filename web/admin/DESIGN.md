---
name: Fullsend Admin
description: Design system for the Fullsend Admin SPA — GitHub Primer-derived tokens, component patterns, and UI rules for AI agents generating Svelte 5 components.
version: 0.1.0
colors:
  primary: "#0969da"
  fg-default: "#24292f"
  fg-muted: "#444444"
  fg-on-emphasis: "#ffffff"
  fg-accent: "#0969da"
  fg-danger: "#cf222e"
  fg-warning: "#9a6700"
  fg-success: "#1a7f37"
  bg-default: "#ffffff"
  bg-subtle: "#f4f4f4"
  bg-emphasis: "#24292f"
  bg-danger-subtle: "#fff0f0"
  bg-warning-subtle: "#fff8e1"
  bg-success-subtle: "#dafbe1"
  border-default: "#d0d7de"
  border-muted: "#cccccc"
  border-danger: "#cf222e"
  border-warning: "#d4a72c"
  border-accent: "{colors.primary}"
  btn-hover: "#e8e8e8"
  btn-active: "#d8d8d8"
  btn-primary-hover: "#32383f"
  btn-primary-active: "#1c2025"
  link-hover: "#0550ae"
  link-active: "#033d8b"
typography:
  body:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.4
  secondary:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif"
    fontSize: "0.9rem"
    fontWeight: 400
    lineHeight: 1.4
  small:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1.4
  mono:
    fontFamily: "ui-monospace, 'SFMono-Regular', 'SF Mono', Menlo, Consolas, 'Liberation Mono', monospace"
    fontSize: "0.85rem"
    fontWeight: 400
    lineHeight: 1.4
  heading:
    fontFamily: "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif"
    fontSize: "1rem"
    fontWeight: 600
    lineHeight: 1.4
spacing:
  xs: "0.25rem"
  sm: "0.5rem"
  md: "0.75rem"
  lg: "1rem"
  xl: "1.5rem"
rounded:
  sm: 4px
  md: 6px
  lg: 8px
components:
  button:
    backgroundColor: "{colors.bg-subtle}"
    textColor: "{colors.fg-default}"
    typography: "{typography.secondary}"
    rounded: "{rounded.md}"
    padding: "0.4rem 0.75rem"
  button-hover:
    backgroundColor: "{colors.btn-hover}"
  button-active:
    backgroundColor: "{colors.btn-active}"
  button-primary:
    backgroundColor: "{colors.bg-emphasis}"
    textColor: "{colors.fg-on-emphasis}"
  button-primary-hover:
    backgroundColor: "{colors.btn-primary-hover}"
  button-primary-active:
    backgroundColor: "{colors.btn-primary-active}"
  banner:
    typography: "{typography.secondary}"
    padding: "0.75rem 1rem"
  banner-error:
    backgroundColor: "{colors.bg-danger-subtle}"
    textColor: "{colors.fg-danger}"
  banner-warning:
    backgroundColor: "{colors.bg-warning-subtle}"
    textColor: "{colors.fg-warning}"
  card:
    backgroundColor: "{colors.bg-default}"
    rounded: "{rounded.lg}"
    padding: "1rem"
  spinner:
    size: "1.1rem"
    rounded: "50%"
---

# Fullsend Admin Design System

## Overview

The Fullsend Admin SPA is an internal operations tool for managing GitHub App installations and org-level layer analysis. The visual language derives from GitHub's Primer design system — neutral, information-dense, and utilitarian. The brand personality is **trustworthy and precise**, not playful or decorative.

This is a tool for engineers managing CI/CD infrastructure. Every pixel should serve information density or interaction clarity.

## Colors

The palette is GitHub Primer-derived. Use semantic token names, never raw hex values.

| Token | Hex | Usage |
|-------|-----|-------|
| `--fg-default` | `#24292f` | Primary text, headings, emphasis borders |
| `--fg-muted` | `#444444` | Secondary text, status labels, timestamps |
| `--fg-on-emphasis` | `#ffffff` | Text on dark/emphasis backgrounds |
| `--fg-accent` | `#0969da` | Links, interactive text, focus rings |
| `--fg-danger` | `#cf222e` | Error text, destructive action labels |
| `--fg-warning` | `#9a6700` | Warning text, caution indicators |
| `--fg-success` | `#1a7f37` | Success text, healthy status |
| `--bg-default` | `#ffffff` | Page background |
| `--bg-subtle` | `#f4f4f4` | Secondary buttons, subtle card backgrounds |
| `--bg-emphasis` | `#24292f` | Primary buttons, header backgrounds |
| `--bg-danger-subtle` | `#fff0f0` | Error banners, danger backgrounds |
| `--bg-warning-subtle` | `#fff8e1` | Warning banners |
| `--bg-success-subtle` | `#dafbe1` | Success banners |
| `--border-default` | `#d0d7de` | Card borders, dividers, table borders |
| `--border-muted` | `#cccccc` | Subtle separators, input borders |
| `--border-danger` | `#cf222e` | Error state borders |
| `--border-accent` | `#0969da` | Focus rings, active state borders |

### Do's and Don'ts

- **Do** use `--fg-danger` for error text and `--bg-danger-subtle` for error banners together.
- **Do** use `--border-accent` with `outline-offset: 2px` for all focus-visible states.
- **Don't** use raw hex values. Always reference CSS custom properties.
- **Don't** introduce new colors outside this palette without updating this file.

## Typography

| Element | Font | Size | Weight |
|---------|------|------|--------|
| Body text | system-ui stack | `1rem` | 400 |
| Secondary text | system-ui stack | `0.9rem` | 400 |
| Small labels, tags | system-ui stack | `0.75rem` | 400 |
| Monospace (code, IDs) | ui-monospace stack | `0.85rem` | 400 |
| Section headings | system-ui stack | `1rem` | 600 |
| Page title | system-ui stack | `1rem` | 700 |

### Do's and Don'ts

- **Do** use the monospace stack for GitHub logins, commit SHAs, repo names, and technical identifiers.
- **Don't** use font sizes outside the defined scale. If you need a size not listed, it's a signal to reconsider the hierarchy.
- **Don't** use bold for emphasis in body text — use color (`--fg-accent` or `--fg-danger`) to distinguish important information.

## Layout

The admin SPA uses a single-column layout constrained to a readable width.

| Property | Value | Rationale |
|----------|-------|-----------|
| Content max-width | `42rem` | Optimal line length for data-dense views |
| Page padding | `1rem` | Consistent gutter on all sides |
| Header padding | `0.75rem 1rem` | Slightly tighter than content |
| Component gap | `0.75rem` | Default gap in flex containers |
| Section spacing | `1.5rem` margin-top | Between major page sections |

### Responsive Behavior

The `42rem` max-width naturally accommodates mobile viewports without media queries. For components that need responsive adjustments:

- Below `640px`: Stack horizontal layouts vertically using `flex-wrap: wrap`.
- Below `480px`: Reduce padding to `0.75rem`, hide non-essential secondary text.
- Always test at three widths: `1280px`, `768px`, `375px`.

### Do's and Don'ts

- **Do** use flexbox with `gap` for all layouts. Never use margin hacks for spacing between siblings.
- **Do** use `flex-wrap: wrap` on any horizontal layout that might exceed the viewport.
- **Don't** use absolute positioning for layout. Reserve it for overlays and popovers only.
- **Don't** set fixed widths on content containers. Use `max-width` with percentage or rem values.

## Elevation & Depth

The design is intentionally flat. Elevation is used sparingly and only to separate overlaid content from the page surface.

| Level | Shadow | Usage |
|-------|--------|-------|
| None (default) | No shadow | Cards, sections, list rows, buttons — all sit flush on the page |
| Subtle | `0 1px 2px rgba(0, 0, 0, 0.08)` | Popovers and tooltips only |

### Do's and Don'ts

- **Do** use borders (`--border-default`) to define boundaries between content regions.
- **Don't** use box-shadow to create visual hierarchy between on-page elements. Hierarchy comes from color, weight, and position (see Visual Hierarchy section).
- **Don't** stack multiple elevation levels. There is one shadow value for overlays — that's it.

## Shapes

Corner rounding communicates interactivity and containment.

| Radius | Value | Description | Usage |
|--------|-------|-------------|-------|
| `sm` | `4px` | Barely rounded, subtle softening | Input fields, small badges |
| `md` | `6px` | Standard interactive rounding | Buttons, dropdown menus |
| `lg` | `8px` | Container-level rounding | Cards, sections, modal panels |
| Full | `50%` | Circular | Spinners, avatars |

### Do's and Don'ts

- **Do** use `md` (6px) for all interactive elements (buttons, inputs, selects).
- **Do** use `lg` (8px) for content containers (cards, sections).
- **Don't** mix rounding values on adjacent elements — buttons inside a card should all use `md`.
- **Don't** use rounded corners on full-width elements (banners, header bars) — they should be square to signal they span the viewport.

## Motion and Transitions

All state changes that affect layout or visibility should use transitions. Animations are functional, not decorative — they communicate state changes.

| Property | Duration | Easing | Usage |
|----------|----------|--------|-------|
| `opacity` | `150ms` | `ease` | Pending regions, fade-in content |
| `background-color` | `120ms` | `ease` | Button hover/active, row hover |
| `border-color` | `120ms` | `ease` | Input focus, validation states |
| `transform` (spinner) | `700ms` | `linear` | Loading spinners (continuous) |

```css
/* Default transition for interactive elements */
.interactive {
  transition: background-color 120ms ease, border-color 120ms ease, opacity 150ms ease;
}
```

### Reduced Motion

Always wrap continuous animations (spinners) in a reduced-motion query:

```css
@media (prefers-reduced-motion: reduce) {
  .spinner { animation: none; border-top-color: var(--fg-accent); }
  * { transition-duration: 0.01ms !important; }
}
```

### Do's and Don'ts

- **Do** use transitions on hover, focus, and state changes — they signal interactivity.
- **Don't** animate layout properties (`width`, `height`, `top`, `left`) — they trigger reflow. Use `transform` and `opacity` only.
- **Don't** add entrance animations, slide-ins, or decorative motion. This is an ops tool — motion should be invisible unless it communicates state.

## Visual Hierarchy

Information density is high. Use these tools to direct attention:

| Signal | How | When |
|--------|-----|------|
| **Color** | `--fg-danger` or `--fg-warning` on text | Errors, degraded status, action required |
| **Weight** | `font-weight: 600` | Section headings, org names, primary identifiers |
| **Background** | `--bg-danger-subtle` / `--bg-warning-subtle` | Banner-level alerts, row-level status |
| **Opacity** | `0.55` + `pointer-events: none` | Pending/loading regions |
| **Position** | Top of section, above the fold | Critical status, errors — never bury below secondary data |

### Do's and Don'ts

- **Do** put errors and warnings at the top of their section, not at the bottom.
- **Do** use color + icon together for status — never color alone (accessibility).
- **Don't** use elevation (box-shadow) to create hierarchy. The design is flat by intent.
- **Don't** make everything bold. If three things are bold, nothing is bold.

## Components

### Buttons

Two variants: default (secondary) and primary.

```css
/* Default button */
.btn {
  cursor: pointer;
  padding: 0.4rem 0.75rem;
  border: 1px solid var(--border-muted);
  border-radius: 6px;
  background: var(--bg-subtle);
  font: inherit;
  font-size: 0.9rem;
  line-height: 1.4;
  color: var(--fg-default);
  transition: background-color 120ms ease, border-color 120ms ease;
}
.btn:hover {
  background: var(--btn-hover);
  border-color: var(--border-muted);
}
.btn:active {
  background: var(--btn-active);
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* Primary button */
.btn.primary {
  background: var(--bg-emphasis);
  color: var(--fg-on-emphasis);
  border-color: var(--bg-emphasis);
}
.btn.primary:hover {
  background: var(--btn-primary-hover);
}
.btn.primary:active {
  background: var(--btn-primary-active);
}
```

Every button MUST have:
- `type="button"` (unless it's a form submit button)
- A visible focus ring: `outline: 2px solid var(--border-accent); outline-offset: 2px` on `:focus-visible`
- Hover, active, and disabled visual states (see CSS above)
- A loading state if it triggers an async operation: disable the button and show a spinner
- `cursor: pointer` (default), `cursor: not-allowed` (disabled)

### Link Buttons

For inline text actions (dismiss, retry, see details):

```css
.link-btn {
  cursor: pointer;
  border: none;
  background: none;
  padding: 0;
  font: inherit;
  color: var(--fg-accent);
  text-decoration: underline;
  transition: color 120ms ease;
}
.link-btn:hover {
  color: var(--link-hover);
}
.link-btn:active {
  color: var(--link-active);
}
```

### Spinners

One spinner component with size variants. Do not create new spinner implementations.

```css
@keyframes spin {
  to { transform: rotate(360deg); }
}
.spinner {
  display: inline-block;
  border-style: solid;
  border-color: var(--border-muted);
  border-top-color: var(--fg-default);
  border-radius: 50%;
  animation: spin 0.7s linear infinite;
}
.spinner--sm { width: 0.85rem; height: 0.85rem; border-width: 2px; }
.spinner--md { width: 1.1rem; height: 1.1rem; border-width: 2px; }
.spinner--lg { width: 1.5rem; height: 1.5rem; border-width: 2.5px; }
```

Every spinner MUST have:
- `role="status"` on the container
- An `aria-label` describing what is loading
- A corresponding text label visible to sighted users (e.g., "Loading organisations...")

### Banners

For status messages, errors, and warnings at the top of a section:

```css
.banner {
  margin: 0;
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--border-default);
  max-width: 42rem;
  font-size: 0.9rem;
}
.banner--error {
  color: var(--fg-danger);
  background: var(--bg-danger-subtle);
  border-color: var(--border-danger);
}
.banner--warning {
  color: var(--fg-warning);
  background: var(--bg-warning-subtle);
  border-color: var(--border-warning);
}
.banner--success {
  color: var(--fg-success);
  background: var(--bg-success-subtle);
}
```

### Cards / Sections

For bounded content regions (proof sections, org detail panels):

```css
.card {
  padding: 1rem;
  border: 1px solid var(--border-default);
  border-radius: 8px;
  max-width: 42rem;
}
.card h2 {
  margin: 0 0 0.5rem;
  font-size: 1rem;
  font-weight: 600;
}
```

### Lists

For org lists and similar data rows:

- Use semantic `<ul>/<li>` markup
- Each row should have consistent padding (`0.75rem 1rem`)
- Rows with interactive elements should have hover state: `background: var(--bg-subtle)`
- Separate rows with `border-bottom: 1px solid var(--border-default)`

## Accessibility

All components MUST meet WCAG 2.1 AA. Specific requirements:

| Requirement | Implementation |
|-------------|---------------|
| Focus visible | `outline: 2px solid var(--border-accent); outline-offset: 2px` on all interactive elements |
| Screen reader labels | `aria-label` on icon-only buttons, spinners; `.sr-only` class for visually hidden text |
| Semantic HTML | `<button>` for actions, `<a>` for navigation, `<section>` with headings for regions |
| Live regions | `aria-live="polite"` on status text that updates dynamically |
| Busy states | `aria-busy="true"` on containers while loading |
| Keyboard navigation | All interactive elements reachable via Tab; Enter/Space to activate |
| Reduced motion | `@media (prefers-reduced-motion: reduce)` to disable spinner and transition animations |

### Screen-Reader-Only Utility

```css
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
```

## Interaction Patterns

### Loading States

When a button triggers an async operation:
1. Disable the button immediately (`disabled` attribute)
2. Replace button text with spinner + "Loading..." text
3. Re-enable on completion or error

### Error States

- Show error text in `--fg-danger` with a dismiss action
- For per-row errors in lists, show inline below the affected row
- Always provide a recovery action (retry, dismiss, or navigate away)

### Pending/Disabled Regions

When a section is loading or unavailable:
```css
.pending {
  opacity: 0.55;
  pointer-events: none;
}
```
Always pair with `aria-busy="true"` on the container.

## Component Size Rule

No Svelte component file should exceed 150 lines. If a component grows beyond this:

1. Extract reusable UI elements (buttons, spinners, banners) into `lib/components/`
2. Extract business logic (API calls, state machines, caching) into `lib/` TypeScript modules
3. Split complex views into parent + child components with clear prop interfaces

This is the frontend equivalent of Go's convention to split functions over 50 lines.

## File Organization

```
web/admin/src/
  lib/
    components/      ← shared UI components (Button.svelte, Spinner.svelte, Banner.svelte)
    auth/            ← authentication logic
    github/          ← GitHub API client
    layers/          ← layer analysis
    orgs/            ← org data fetching
  routes/            ← page-level components (one per route)
  app.css            ← CSS custom properties (design tokens) + minimal reset
  App.svelte         ← shell, auth gate, nav
```

## Anti-Patterns

- **No god components.** A 1000+ line Svelte file with interleaved script, markup, and styles is a refactoring signal.
- **No copy-pasted spinners.** Use the shared Spinner component with size props.
- **No duplicate `.btn` definitions.** Use the shared Button component or import shared styles.
- **No raw hex colors.** Every color value must reference a CSS custom property from `app.css`.
- **No inline `style` attributes.** Use classes and CSS custom properties.
- **No magic spacing values.** Use the spacing scale tokens (`--space-xs` through `--space-xl`).
