---
name: spec-start-github
description: >-
  Use when a GitHub issue is the canonical prompt for a spec-start run: fetch
  the issue, work on agent/<issue>-spec-* branches, commit planning artifacts,
  then open a PR locally or stop before push when a harness post-script owns
  GitHub writes. Pair with spec-start for the headless planning procedure.
# Cursor Agent Skills: prefer explicit @-style invocation; other tooling may ignore.
disable-model-invocation: true
---

# spec-start-github (issue-driven spec, artifacts on a PR)

Use this skill **together with `spec-start`**: mount or invoke both in the same session so the headless planning checklist, hard-gate, `spec.md` / `qna.md` shape, and self-review are defined in **one place** (`spec-start`). This document only adds **GitHub + git + PR** mechanics. **Do not** duplicate or paraphrase `spec-start` at length.

If the user’s prompt asks for another workflow or skill, follow that; this skill does not mandate unrelated skills.

## Relationship to `spec-start`

1. Use the steps below to fetch the issue, decide canonical prompt text, and prepare the branch.
2. Run the **full headless spec pass exactly as `spec-start` specifies** (topic directory, files, gates, final reply expectations). Treat the issue title and body (plus comment context per below) as the **prompt** you would otherwise have read from chat.

## Inputs

- **`ISSUE_NUMBER`** — required for `gh issue view` when not inferred from `GITHUB_ISSUE_URL`.
- **`GITHUB_ISSUE_URL`** — optional HTML URL; when set, it must match `ISSUE_NUMBER` and the repo you are working in (same checks fullsend pre-scripts use: same owner/repo, same issue number).
- **`TARGET_BRANCH`** — optional; default integration branch is usually `main` (discover with `git rev-parse --abbrev-ref origin/HEAD` if unsure).

Harness authors may set **`SPEC_START_GITHUB_POST_SCRIPT_PUSHES=1`** (or equivalent session wording) so “post-automation opens the PR” is explicit; **primary** signal remains **session / wrapper instructions** that say push and `gh pr create` are out of bounds.

## Step 1 — Fetch the issue

```bash
echo "::notice::STEP 1: Fetch GitHub issue"
gh issue view "${ISSUE_NUMBER}" --json number,title,body,labels,comments,state,author
```

If the issue is closed (and the run is not explicitly for historical spec capture), stop unless the user or harness says otherwise.

## Step 2 — Canonical prompt

- **Primary intent:** issue **title** and **body**. Build an internal prompt string from them; that text is authoritative over incidental chat in the session.
- **Comments:** use as supporting context (triage notes, clarifications). Do **not** let comments override the issue body unless the body explicitly points at a specific comment.

## Step 3 — Branch (collision-aware)

Implementation-style agents often use `agent/<issue>-<short-slug>`. For spec-only work, use:

`agent/<ISSUE_NUMBER>-spec-<short-slug>`

Use **2–4 lowercase hyphenated words** from the issue title for `<short-slug>`. Fetch `origin` and branch from **`TARGET_BRANCH`** or the repo default.

**Existing work:** if a branch matching `agent/<ISSUE_NUMBER>-spec-*` already exists, inspect it. If an **open** PR already targets it, **stop** without new commits (same scope guard as implementation agents: avoid piling on under review). If the branch exists but no open PR, check it out and continue only to fix verification gaps the user or harness asked for—do not rewrite unrelated spec prose.

## Step 4 — Headless spec pass (`spec-start`)

Follow **`spec-start`** for the uninterrupted planning pass and for **all** artifact rules (paths under `docs/plans/`, `spec.md`, `qna.md`, optional assets, commit-only-if-asked default from `spec-start`, and final reply bullets).

In `spec.md` **Context**, include the issue URL, number, and title.

## Step 5 — Commit

This skill assumes a **commit is intended** whenever you are on a feature branch for a publish path (local PR or harness handoff). That overrides `spec-start`’s default “commit only if explicitly asked” for **chat-only** runs.

Stage **only** files under the topic directory you created (and optional assets there). Use the repo’s commit conventions if you can infer them; include **`Refs #<ISSUE_NUMBER>`** in the commit body unless merging this PR should close the issue (then **`Closes #N`** is appropriate).

Run **`scan-secrets`** on changed paths if available; run **pre-commit** on those files if the repo uses it and time allows—otherwise disclose skips. No production test suite is required for docs-only output (`spec-start` scope).

## Step 6 — Publish (local vs harness)

| Mode | When | Agent actions |
|------|------|-----------------|
| **Local** | Default when session does **not** say automation owns push/PR | `git push -u origin` the feature branch (use existing auth). Then **`gh pr create`** against the integration branch with: PR title aligned with repo conventions; body with **link to the issue**, **`Refs #N`** (or `Part of #N`) by default so the issue stays open for implementation work, **topic directory path**, one-paragraph **recommended approach**, **top three open questions** from `qna.md`, and a short “what to review” (`spec.md` / `qna.md`). Avoid **`Closes #N`** unless closing the issue on merge is intended. |
| **Harness** | Session or wrapper says **post-automation** opens the PR / push | **Do not** `git push`. **Do not** `gh pr create` or `gh pr edit`. Stop after commit; in the final reply, give the **suggested PR title** and **full PR body** so a post-script or human can open the PR. |

Some runner scripts append **`Closes #N`** to PR bodies automatically. Harness authors should prefer **`Refs`** for spec-only PRs when they control templates.

After the PR is open, do **not** auto-invoke **`spec-refine-github`** unless the session asks for a refine pass. When reviewers leave PR or issue comments (or request changes), use **`spec-refine-github`** to ingest that feedback and update `spec.md` / `qna.md` on the same branch (see that skill for harness vs local push rules).

## Aftercare

Mirror `spec-start`: list topic path, summarize the recommended approach, surface top open questions. If harness mode, also paste the suggested PR title and body. After review on the PR, **`spec-refine-github`** is the follow-up skill that merges feedback back into `spec.md` / `qna.md`.
