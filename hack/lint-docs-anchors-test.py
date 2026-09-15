"""Tests for hack/lint-docs-anchors (VitePress heading fragment linter)."""

from __future__ import annotations

import runpy
from pathlib import Path
from types import SimpleNamespace

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = REPO_ROOT / "hack" / "lint-docs-anchors"


def load_mod():
    # The hook is an extensionless executable (like the other hack/ linters).
    return SimpleNamespace(**runpy.run_path(str(SCRIPT), run_name="lint_docs_anchors"))


mod = load_mod()


# --- slugify: lockstep with @mdit-vue/shared / VitePress 1.6.4 ---


def test_slugify_punctuation_heading_matches_vitepress_not_lychee():
    """The PR #7187 flip-flop case: dots and parens become hyphens."""
    heading = "config.base.yaml (vendor preset)"
    assert mod.slugify(heading) == "config-base-yaml-vendor-preset"
    assert mod.slugify(heading) != "configbaseyaml-vendor-preset"
    assert mod.githubish_slug(heading) == "configbaseyaml-vendor-preset"


def test_slugify_leading_digit_gets_underscore_prefix():
    assert mod.slugify("1. Webhook + dispatch service") == "_1-webhook-dispatch-service"


def test_slugify_collapses_consecutive_specials():
    assert mod.slugify("CI/CD") == "ci-cd"
    assert mod.slugify("AGENTS.md") == "agents-md"
    assert mod.slugify("Forge-sdlc/forge") == "forge-sdlc-forge"


def test_slugify_keeps_em_dash():
    # em-dash is not in @mdit-vue/shared's rSpecial set
    assert mod.slugify("Event semantics — input only") == "event-semantics-—-input-only"


def test_explicit_attr_id():
    text, explicit = mod.heading_text_and_explicit_id(
        "config.base.yaml (vendor preset) {#config-base-yaml-vendor-preset}"
    )
    assert text == "config.base.yaml (vendor preset)"
    assert explicit == "config-base-yaml-vendor-preset"


def test_inline_code_in_heading_is_stripped():
    text, explicit = mod.heading_text_and_explicit_id("`config.base.yaml` (vendor preset)")
    assert explicit is None
    assert text == "config.base.yaml (vendor preset)"
    assert mod.slugify(text) == "config-base-yaml-vendor-preset"


def _write_tree(tmp_path: Path, files: dict[str, str]) -> Path:
    docs = tmp_path / "docs"
    for rel, content in files.items():
        path = docs / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
    return docs


def _check(tmp_path: Path, files: dict[str, str]) -> list:
    docs = _write_tree(tmp_path, files)
    md_files = sorted(docs.rglob("*.md"))
    return mod.check_files(md_files, tmp_path)


def test_accepts_vitepress_slug_for_divergent_heading(tmp_path: Path):
    findings = _check(
        tmp_path,
        {
            "guide.md": (
                "# Guide\n\n"
                "See [preset](#config-base-yaml-vendor-preset).\n\n"
                "### config.base.yaml (vendor preset)\n"
            )
        },
    )
    assert findings == []


def test_rejects_lychee_guess_for_divergent_heading(tmp_path: Path):
    findings = _check(
        tmp_path,
        {
            "guide.md": (
                "# Guide\n\n"
                "See [preset](#configbaseyaml-vendor-preset).\n\n"
                "### config.base.yaml (vendor preset)\n"
            )
        },
    )
    assert len(findings) == 1
    assert findings[0].fragment == "configbaseyaml-vendor-preset"
    assert findings[0].hint is not None
    assert "config-base-yaml-vendor-preset" in findings[0].hint


def test_rejects_genuinely_missing_fragment(tmp_path: Path):
    findings = _check(
        tmp_path,
        {"guide.md": "# Guide\n\nSee [missing](#no-such-heading).\n"},
    )
    assert len(findings) == 1
    assert findings[0].fragment == "no-such-heading"


def test_html_id_is_accepted(tmp_path: Path):
    findings = _check(
        tmp_path,
        {
            "guide.md": (
                "# Guide\n\n"
                '<a id="customization-vocabulary"></a>\n\n'
                "See [vocab](#customization-vocabulary).\n"
            )
        },
    )
    assert findings == []


def test_explicit_heading_id_is_accepted(tmp_path: Path):
    findings = _check(
        tmp_path,
        {"guide.md": ("# Guide\n\nSee [pinned](#pinned-id).\n\n### Some heading {#pinned-id}\n")},
    )
    assert findings == []


def test_ignores_links_in_inline_code(tmp_path: Path):
    findings = _check(
        tmp_path,
        {"guide.md": "# Guide\n\nUse `[text](#no-such-heading)` in prose.\n"},
    )
    assert findings == []


def test_ignores_headings_and_links_in_fenced_code(tmp_path: Path):
    findings = _check(
        tmp_path,
        {
            "guide.md": (
                "# Guide\n\n```md\n## Not a real heading\n[broken](#not-a-real-heading)\n```\n"
            )
        },
    )
    assert findings == []


def test_cross_file_relative_fragment(tmp_path: Path):
    ok = _check(
        tmp_path,
        {
            "a.md": "# A\n\nSee [b](b.md#config-base-yaml-vendor-preset).\n",
            "b.md": "# B\n\n### config.base.yaml (vendor preset)\n",
        },
    )
    assert ok == []
    bad = _check(
        tmp_path,
        {
            "a.md": "# A\n\nSee [b](b.md#configbaseyaml-vendor-preset).\n",
            "b.md": "# B\n\n### config.base.yaml (vendor preset)\n",
        },
    )
    assert len(bad) == 1
    assert bad[0].fragment == "configbaseyaml-vendor-preset"


def test_duplicate_headings_get_suffix(tmp_path: Path):
    findings = _check(
        tmp_path,
        {
            "guide.md": (
                "# Guide\n\n"
                "## Status\n\n"
                "First [status](#status) and second [status](#status-1).\n\n"
                "## Status\n"
            )
        },
    )
    assert findings == []


def test_skips_excluded_archived_roadmap(tmp_path: Path):
    findings = _check(
        tmp_path,
        {"archived-roadmaps/2026-07.md": ("# Old\n\nSee [missing](#does-not-exist).\n")},
    )
    assert findings == []


def test_current_docs_tree_matches_vitepress_ids():
    """Full-tree gate: every docs/ fragment must match a VitePress id."""
    files = mod.discover_docs(REPO_ROOT)
    assert files, "expected to discover docs/ markdown files"
    findings = mod.check_files(files, REPO_ROOT)
    assert findings == [], "\n".join(f.format(REPO_ROOT) for f in findings)
