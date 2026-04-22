#!/usr/bin/env python3
"""Process fix-result.json and post a summary comment on the PR.

Reads the structured output from the fix agent and posts a summary comment
documenting what was fixed and what was disagreed with.

Usage:
    process-fix-result.py <fix-result.json> <owner/repo> <pr-number> [--dry-run]

Exit codes:
    0 — summary posted (or dry-run completed)
    1 — invalid arguments or unreadable input file
"""

import json
import subprocess
import sys


def build_summary_body(data):
    """Build the markdown summary comment body."""
    summary = data.get("summary", "Fix agent completed.")
    decision_points = data.get("decision_points", [])
    tests_passed = data.get("tests_passed", False)
    trigger = data.get("trigger_source", "unknown")
    iteration = data.get("iteration", 1)
    actions = data.get("actions", [])

    fixed = [a for a in actions if a.get("type") == "fix"]
    disagreed = [a for a in actions if a.get("type") == "disagree"]

    fixes_text = ""
    if fixed:
        items = []
        for i, a in enumerate(fixed, 1):
            finding = a.get("finding", "unknown")
            desc = a.get("description", "")
            path = a.get("path", "")
            path_suffix = f" (`{path}`)" if path else ""
            items.append(f"{i}. **{finding}**{path_suffix}: {desc}")
        fixes_text = "\n".join(items)

    disagree_text = ""
    if disagreed:
        items = []
        for i, a in enumerate(disagreed, 1):
            finding = a.get("finding", "unknown")
            reason = a.get("reason", "No reason provided.")
            items.append(f"{i}. **{finding}**: {reason}")
        disagree_text = "\n".join(items)

    dp_text = ""
    if decision_points:
        dp_items = []
        for dp in decision_points:
            desc = dp.get("description", "")
            rationale = dp.get("rationale", "")
            alts = dp.get("alternatives", [])
            alt_text = ", ".join(alts) if alts else "none considered"
            dp_items.append(f"- {desc} (alternatives: {alt_text}; rationale: {rationale})")
        dp_text = (
            "\n<details>\n<summary>Decision points</summary>\n\n"
            + "\n".join(dp_items)
            + "\n</details>\n"
        )

    tests_str = "passed" if tests_passed else "**failed**"

    sections = [f"### \U0001f527 Fix agent \u2014 iteration {iteration} ({trigger}-triggered)\n"]
    sections.append(f"{summary}\n")

    if fixes_text:
        sections.append(f"**Fixed ({len(fixed)}):**\n{fixes_text}\n")

    if disagree_text:
        sections.append(f"**Disagreed ({len(disagreed)}):**\n{disagree_text}\n")

    sections.append(f"**Tests:** {tests_str}")

    if dp_text:
        sections.append(dp_text)

    sections.append(
        '\n<sub>Updated by <a href="https://github.com/fullsend-ai/fullsend">'
        "fullsend</a> fix agent</sub>"
    )

    return "\n".join(sections)


MAX_COMMENT_LENGTH = 32768


def post_summary(repo, pr_number, body, dry_run=False):
    """Post a summary comment on the PR."""
    if len(body) > MAX_COMMENT_LENGTH:
        truncation_notice = "\n\n*[truncated — output exceeded 32KB]*"
        body = body[: MAX_COMMENT_LENGTH - len(truncation_notice)] + truncation_notice
        print(f"::warning::Comment body truncated from {len(body)} to {MAX_COMMENT_LENGTH} chars")
    if dry_run:
        print(f"  [dry-run] Would post PR summary ({len(body)} chars)")
        return True
    try:
        subprocess.run(
            ["gh", "pr", "comment", str(pr_number), "--repo", repo, "--body", body],
            check=True,
            capture_output=True,
            text=True,
        )
        return True
    except subprocess.CalledProcessError as e:
        print(
            f"::warning::Failed to post PR summary: {e.stderr}",
            file=sys.stderr,
        )
        return False


def main(argv=None):
    if argv is None:
        argv = sys.argv[1:]

    if len(argv) < 3:
        print(
            "Usage: process-fix-result.py <fix-result.json> <owner/repo> <pr-number> [--dry-run]",
            file=sys.stderr,
        )
        return 1

    result_file = argv[0]
    repo = argv[1]
    pr_number = argv[2]
    dry_run = "--dry-run" in argv

    try:
        with open(result_file) as f:
            data = json.load(f)
    except (json.JSONDecodeError, OSError) as e:
        print(f"::error::Cannot read {result_file}: {e}", file=sys.stderr)
        return 1

    actions = data.get("actions", [])
    valid_types = {"fix", "disagree"}
    for a in actions:
        atype = a.get("type", "")
        if atype not in valid_types:
            print(f"::warning::Unknown action type '{atype}' — ignored", file=sys.stderr)
    fixed = sum(1 for a in actions if a.get("type") == "fix")
    disagreed = sum(1 for a in actions if a.get("type") == "disagree")
    print(f"Processed: {fixed} fixed, {disagreed} disagreed")

    summary_body = build_summary_body(data)
    post_summary(repo, pr_number, summary_body, dry_run)

    return 0


if __name__ == "__main__":
    sys.exit(main())
