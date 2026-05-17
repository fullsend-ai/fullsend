# Q&A — web icon fonts for UI

## Q-01 — Which icon font family should be canonical?

- **Kind:** open
- **Detail:** The issue asks for icon fonts but does not pick a vendor. Triage suggested Material Symbols, Font Awesome, Lucide, Heroicons, or other. Lucide/Heroicons are commonly shipped as SVG components; adapting them to a font may be non-standard compared to first-party variable fonts.
- **Suggested resolution:** Record the chosen family and self-host decision in the implementation ADR or PR description.

### Answer — @ifireball — PR review (Q-01 options)

https://github.com/fullsend-ai/fullsend/pull/907#discussion_r3239432525

<!-- gh-rc:3239432525 ts:2026-05-14T06:08:32Z -->
<!-- gh-thread:3239432525 -->

Please suggest a few options, provide factors for comparison such as licensing constraints, file size, icon coverage and stylistic match, and select one of the options as the most recommended one.

### Answer — synthesis — font family comparison

| Option | Licensing (typical self-host) | Subset WOFF2 size (order of magnitude) | Icon coverage | Stylistic match to current UI | Fit for “icon font” issue |
|--------|------------------------------|----------------------------------------|---------------|------------------------------|---------------------------|
| **Material Symbols** (variable) | Apache 2.0; Google Fonts redistribution terms when self-hosting | ~100–250 KB subset; full variable font larger | Very large (Material Design set) | Strong: docs chrome already resembles MD close/menu/folder glyphs | **Best** — native variable font, ligatures, FILL/weight axes |
| **Font Awesome 6 Free** (webfont) | SIL OFL 1.1 + CC BY 4.0 for icons; brand icons separate | ~70–150 KB tight subset | Large general + brands pack | Good for familiar “folder/file/menu” metaphors | Good fallback; brand glyphs need separate policy |
| **Font Awesome 6 Pro** | Paid license | Similar per subset | Largest FA catalog | Same | Only if org already has Pro seats |
| **Tabler Icons** (font build) | MIT | Moderate per subset | Large outline set | Neutral outline style | Viable; less axis control than Material Symbols |
| **Lucide / Heroicons as font** | MIT (SVG); font builds are community/third-party | Varies | Matches SVG set when converted | Close to many modern apps | **Weak fit** — primary distribution is SVG/React; font path is non-standard maintenance |

**Recommendation:** **Material Symbols** (variable, self-hosted WOFF2 subset) as the canonical pack, with **Font Awesome 6 Free** documented as the fallback if Material licensing/hosting is blocked. Implementation should still record the final choice in an ADR.

## Q-02 — What stays SVG on purpose?

- **Kind:** assumption
- **Detail:** Scope is **UI chrome icons only**—not graph geometry, not Mermaid output, not dynamically generated visualization shapes.
- **Confidence:** high
- **Blast radius if wrong:** Wasted effort encoding graph nodes or diagram output as font glyphs.

### Answer — @ifireball — PR review (scope confirm)

https://github.com/fullsend-ai/fullsend/pull/907#discussion_r3239446421

<!-- gh-rc:3239446421 ts:2026-05-14T06:12:15Z -->
<!-- gh-thread:3239446421 -->

I confirm - this is only about icons.

## Q-03 — How strict is “no inline SVG” for admin/docs chrome?

- **Kind:** scope
- **Detail:** **Pragmatic boundary** (confirmed): migrate repeated UI chrome icons to the font system; allow inline SVG or `<img>` where brand guidelines or poor font fit apply.
- **Proposed handling:** Document allowed exceptions (see `spec.md` inventory “Keep SVG” column).

### Answer — @ifireball — PR review (pragmatic confirm)

https://github.com/fullsend-ai/fullsend/pull/907#discussion_r3239450368

<!-- gh-rc:3239450368 ts:2026-05-14T06:13:17Z -->
<!-- gh-thread:3239450368 -->

I confirm using the pragmatic approach.

## Q-04 — Subsetting and build-time manifest ownership

- **Kind:** open
- **Detail:** Subsetting reduces bytes but needs an owner for the manifest of allowed icon names and CI enforcement.
- **Suggested resolution:** Web platform owner defines manifest location (for example colocated with `UiIcon`) and adds a small unit test or script invoked from `npm test` / `make lint` as appropriate.
