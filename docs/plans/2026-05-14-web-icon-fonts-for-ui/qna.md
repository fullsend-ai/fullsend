# Q&A — web icon fonts for UI

## Q-01 — Which icon font family should be canonical?

- **Kind:** open
- **Detail:** The issue asks for icon fonts but does not pick a vendor. Triage suggested Material Symbols, Font Awesome, Lucide, Heroicons, or other. Lucide/Heroicons are commonly shipped as SVG components; adapting them to a font may be non-standard compared to first-party variable fonts.
- **Suggested resolution:** Product owner picks one primary family (variable font preferred) and one fallback; record license + self-host decision in the implementation ADR or PR description.

### Answer — example only (template)

_(This `###` block demonstrates the answer shape for future discussion threads. Replace with a real decision link or permalink when available.)_

Optional permalink line: `https://github.com/fullsend-ai/fullsend/issues/818#issuecomment-4428555502`

## Q-02 — What stays SVG on purpose?

- **Kind:** assumption
- **Detail:** Graph visualization and other data-bound vector art should remain SVG (or canvas) rather than being forced into a font.
- **Confidence:** high
- **Blast radius if wrong:** Wasted effort trying to encode complex paths as fonts, or broken visuals if graph code is “simplified” incorrectly.

## Q-03 — How strict is “no inline SVG” for admin/docs chrome?

- **Kind:** scope
- **Detail:** Strict reading: all static chrome icons move to the font system. Pragmatic reading: brand marks that must follow third-party guidelines may remain inline SVG or `<img>` even when a font glyph exists.
- **Proposed handling:** Adopt the pragmatic boundary; list allowed SVG exceptions in developer docs to avoid drift.

## Q-04 — Subsetting and build-time manifest ownership

- **Kind:** open
- **Detail:** Subsetting reduces bytes but needs an owner for the manifest of allowed icon names and CI enforcement.
- **Suggested resolution:** Web platform owner defines manifest location (for example colocated with `UiIcon`) and adds a small unit test or script invoked from `npm test` / `make lint` as appropriate.

## Q-05 — Fork / sparse web tree vs upstream triage inventory

- **Kind:** assumption
- **Detail:** This spec references triage’s list of SVG-heavy areas; the local checkout used for spec-start may have fewer Svelte files than upstream at times. Implementation should re-audit `web/**` for actual `<svg>` usage at merge time.
- **Confidence:** medium
- **Blast radius if wrong:** Missed migration targets or unnecessary font work on unused components.
