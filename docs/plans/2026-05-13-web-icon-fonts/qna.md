# Q&A — web icon font migration (#818)

## Assumptions

1. **“Icon font” means a webfont with ligatures or codepoints**, not necessarily every third-party “icon solution” that still ships as inline SVG at runtime. **Confidence:** high. **Blast radius if wrong:** we might pivot to a tree-shaken SVG sprite approach; still meets styling goals but not the letter of the issue.

2. **Mermaid-generated SVG inside doc bodies is not in scope** for replacement by an icon font; triage mentioned it as context for “dynamic SVG,” not as UI chrome. **Confidence:** medium. **Blast radius:** product could later ask to restyle diagrams; that would be a separate renderer/design effort.

3. **Both `web/docs` and `web/admin` should follow the same font choice** for consistency and cache reuse. **Confidence:** medium. **Blast radius:** if admin must stay ultra-minimal, it could load a subset while docs loads the full variable font—more complexity.

4. **Self-hosting the font via the repo** is acceptable for licensing and CSP. **Confidence:** medium. **Blast radius:** if legal prefers Google CDN, adjust delivery and CSP docs only.

## Open questions

1. **Is Material’s visual language acceptable for Fullsend’s docs and admin?** If not, do we prefer **Font Awesome**, a **custom subset**, or another OFL font family? *Why it matters:* drives dependency and look-and-feel. *Resolve:* short design review or maintainer vote; spike one screen with Material and one with FA webfont.

2. **GitHub mark: font glyph vs retained SVG?** Brand guidelines may forbid generic “github-ish” shapes or require specific proportions. *Why it matters:* blocks “100% icon font” purity. *Resolve:* check GitHub logo rules; if needed, document a single **brand exception** component.

3. **Should we introduce a tiny shared package** (e.g. `web/shared/icons`) vs duplicated CSS imports in each Vite app? *Why it matters:* DRY vs coupling between unrelated apps. *Resolve:* implementer preference after seeing import ergonomics in Vite.

4. **CDN vs self-host for the font files?** *Why it matters:* performance, privacy, offline dev, CSP. *Resolve:* align with existing Cloudflare / worker headers; default to self-host unless policy says otherwise.

5. **Do we need automated visual regression** for icon alignment (line-height / baseline nits)? *Why it matters:* font metrics differ from SVG box sizing. *Resolve:* only if the repo already runs Playwright screenshot tests for these surfaces; otherwise manual checklist in the implementation PR.

## Scope / decomposition

Single coherent spec: **static UI chrome icons** in the two Svelte apps named in triage and confirmed in the tree. **Explicitly excluded:** Mermaid diagram SVG, arbitrary user-authored SVG in markdown, and any future graph widgets unless a new issue expands scope.

If implementation work gets large, split PRs as **(a)** font + CSS + docs shell, **(b)** tree nav, **(c)** admin sign-in, each still referencing **Refs #818**.
