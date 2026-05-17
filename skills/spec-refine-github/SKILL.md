---
name: spec-refine-github
description: >-
  Use when a GitHub spec PR needs review and PR comments ingested into spec.md
  and qna.md: prepare comments.md from GitHub (with edit detection and qna.md
  placement rules), run spec-refine phases, commit spec.md and qna.md only (never
  comments.md), then push and reply on threads—or defer those writes to harness
  post-automation. Pair with spec-refine and spec-start for formats.
# Cursor Agent Skills: prefer explicit @-style invocation; other tooling may ignore.
disable-model-invocation: true
---

# spec-refine-github (PR comments into spec and Q&A, push updates)

Use this skill **together with `spec-refine`** (and **`spec-start`** for `qna.md` / `spec.md` grammar). This document adds **GitHub + git + PR** mechanics only. **Do not** duplicate the Phase 1–3 procedure in [`spec-refine/SKILL.md`](../spec-refine/SKILL.md)—after you prepare `comments.md` / `qna.md` from GitHub data, run that file’s phases **in order** on the resolved topic directory.

When a session only needs local planning files without GitHub, use **`spec-refine`** alone.

## Relationship to `spec-refine` and `spec-start-github`

1. **Eligibility and ingest** (this skill) — checkout PR head, confirm **exactly one** touched `docs/plans/YYYY-MM-DD-<topic>/` tree, pull comments from GitHub, write **`comments.md`** and/or append **`###`** answers to **`qna.md`** per the rules below.
2. **Refine pass** — follow **`spec-refine`** through Phase 1 → Phase 2 → Phase 3 (including deleting empty `comments.md` when that skill says to).
3. **Publish** — `git add` only publishable paths, `git commit`; then **`git push`** and **review-thread replies** only when **local mode** (see **Harness vs local**). This skill **overrides** `spec-refine`’s default “documentation only / commit only if asked” for **GitHub-bound runs**: committing the topic’s `spec.md` / `qna.md` after a successful refine is **in scope** when this skill is invoked for a PR refresh.

If **`spec-start-github`** exists in the repo, treat its **branch naming** (`agent/*-spec-*`) and **harness vs local** language as **hints** for the same automation family—not as the only way to qualify a PR.

## Harness vs local (mirror `spec-start-github`)

**Primary signal:** session or wrapper instructions.

| Mode | When | Agent must |
|------|------|------------|
| **Harness** | Session or wrapper says **post-automation owns** `git push` and/or **GitHub API writes** (thread replies) | After **`git commit`**, **do not** `git push`. **Do not** post review-thread replies via `gh api`. Emit a **handoff**: branch name, remote, `git push` suggestion, and copy-pastable `gh api` examples for thread replies (REST URLs, JSON bodies, thread / comment IDs). |
| **Local** | No such restriction | May **`git push`** the PR head branch. May post **optional** short replies on review threads (Step F). |

Harness authors may set an env var such as **`SPEC_REFINE_GITHUB_POST_SCRIPT_PUSHES=1`** (or equivalent session wording) so ownership of pushes is explicit—**session text still wins** when it conflicts.

## Inputs

- **`PR_NUMBER`** or **PR URL** — required unless inferred from `gh pr status` / current branch’s tracking PR.
- **`gh`** CLI authenticated to **read** the PR always; **write** (push, create replies) only in **local mode**.

## Step 1 — Resolve PR and checkout

```bash
gh pr view "${PR_NUMBER}" --json number,url,baseRefName,headRefName,headRepositoryOwner,files
```

Check out the PR head (typical pattern):

```bash
gh pr checkout "${PR_NUMBER}"
```

Confirm the working tree matches the PR you intend to update.

## Step 2 — Eligibility guard (one modified topic directory)

Many directories under **`docs/plans/`** may exist on the default branch; that is normal. The guard uses **paths changed by this PR**, not repo-wide counts.

1. List paths touched by the PR vs its base, for example:

   ```bash
   gh pr diff "${PR_NUMBER}" --name-only
   ```

   Alternatively use `gh pr view "${PR_NUMBER}" --json files` and derive paths from the `files` entries (use `path` / `path` + `previousPath` if renames matter).

2. From that set, keep only paths under **`docs/plans/YYYY-MM-DD-<topic>/`** (topic slug convention matches **`spec-start`**: dated folder with kebab-case slug).

3. **Exactly one** distinct topic directory prefix must appear among changed paths. If **zero** or **two or more** such prefixes appear, **stop** with a clear error—do **not** guess.

4. At PR **head**, that directory must contain both **`spec.md`** and **`qna.md`** (required for **`spec-refine`**). If either is missing, **stop**.

Optional: if the head branch matches **`agent/*-spec-*`**, note alignment with **`spec-start-github`**; do **not** require that pattern for eligibility.

## Step 3 — Fetch GitHub comments

Pull enough metadata to support **edit detection** and **threading**:

- **Pull request review comments** (inline on diffs): REST `GET /repos/{owner}/{repo}/pulls/{pull_number}/comments` via `gh api`, or GraphQL equivalent. Capture at least: `id`, `body`, `path`, `line` / `original_line`, `side`, `diff_hunk`, `in_reply_to_id`, `created_at`, `updated_at`, `html_url`, `commit_id` (or current head) as needed for mapping.

- **Issue comments on the PR** (conversation tab): `GET /repos/{owner}/{repo}/issues/{pull_number}/comments` (the PR number is the issue number). Capture `id`, `body`, `updated_at`, `html_url`.

Use `owner` / `repo` from `gh repo view --json nameWithOwner -q .nameWithOwner` or from the PR JSON.

## Step 4 — Ingest into `comments.md` and `qna.md`

### Ingest markers and edited comments

Use **one** stable marker convention everywhere you mirror GitHub text into committed files, for example:

`<!-- gh-rc:COMMENT_ID ts:UPDATED_AT_ISO -->`

placed on the line **immediately after** the `###` heading (or at the start of an inline insert inside an answer body) so reruns can find prior ingests.

- **First time** you ingest comment `COMMENT_ID`: write the marker with the GitHub **`updated_at`** (or body hash) you observed, then the prose.
- **Later runs:** if GitHub’s **`updated_at`** or **body** changed since the marker’s `ts` / recorded snapshot, **update** the previously ingested material (replace or revise)—do **not** skip forever.
- If the comment is **unchanged** and already fully reflected next to its marker, **skip** re-adding duplicate prose.

For **issue** (conversation) comments, you may use a parallel prefix, e.g. `<!-- gh-ic:COMMENT_ID ts:... -->`, in `comments.md` only (still **not** committed if only in scratch—see routing).

### Routing

- **Inline review on `spec.md`** (path matches topic `spec.md`): append a new section to **`comments.md`** following the shape in **`spec-refine`** (e.g. **## For file spec.md lines N** with the reviewer text and permalink). Do **not** commit `comments.md`.

- **Non-inline** PR feedback (issue comments, review summaries without a single line): append to **`comments.md`** as general **`## …`** blocks with attribution when useful.

- **Inline review on `qna.md`** — **placement preserves intent** (map the commented line to the **current** file after checkout):

  1. Resolve which **`## Q-NN — …`** block contains the line (and whether the line is **before the first `###`**, **after the last `###`** for that question, or **inside a `### Answer …` body**, including nested `####`).

  2. **Comment on the question stem** (under `## Q-NN` bullets, before any `###`, or clearly addressing the question row): treat as a **new answer** — append **`### Answer — …`** under that `## Q-NN` per **`spec-start`** (`qna.md` format): unique slug, permalink line under heading when long, then marker + body.

  3. **Comment after all answers** for that `## Q-NN` (below the final `###` of that block): treat as a **new answer** — append another **`### Answer — …`** at the end of that question.

  4. **Comment inside an existing `### Answer …` body**: treat as **discussion on that answer** — prefer **in-place** edits inside that answer (extra paragraph, blockquote, or a child **`#### Thread`** subsection). Avoid silently moving the text to an unrelated top-level `###`. If in-place merge is unsafe, add a **new `###`** that **cites** the target answer heading and states the correction or dispute explicitly.

When multiple GitHub threads map to the same `## Q-NN`, preserve **thread order** where sensible; later activity may supersede earlier per **`spec-refine`** “later overrides earlier” guidance.

## Step 5 — Run `spec-refine`

Execute **Phase 1 → Phase 2 → Phase 3** from [`spec-refine/SKILL.md`](../spec-refine/SKILL.md) on the single topic directory you locked in Step 2.

## Step 6 — Commit (never `comments.md`)

1. **`git add`** with **explicit paths only** — at minimum:

   - `docs/plans/YYYY-MM-DD-<topic>/spec.md`
   - `docs/plans/YYYY-MM-DD-<topic>/qna.md`
   - plus any **tracked** optional assets under that same directory that **`spec-refine`** legitimately changed.

2. **Never** stage **`comments.md`**. If it appears staged, run `git restore --staged -- docs/plans/.../comments.md` (and verify it is not in the commit).

3. Commit with a message consistent with repo conventions, e.g. **“Address PR review feedback”** and reference the driving issue with **`Refs #N`** when known (the PR itself is not an issue number—link the PR URL in the body if helpful).

## Step 7 — Push (local mode only)

In **local mode**, **`git push`** to update the existing PR branch (use the remote tracking branch for the PR head).

In **harness mode**, **do not push**; include exact suggested commands in the final reply.

## Step 8 — Review-thread replies (local mode, optional)

Posting replies is a **GitHub write**, same class as **`git push`**.

- **Harness mode:** **Do not** call reply APIs; output suggested `gh api` / GraphQL mutations and thread / comment IDs.
- **Local mode:** **Best-effort** only—when new or updated answer material clearly corresponds to an open **review thread** on `qna.md`, post a short reply (for example `gh api -X POST repos/{owner}/{repo}/pulls/{pr}/comments/{comment_id}/replies -f body='…'` per [GitHub “Create a reply for a review comment”](https://docs.github.com/rest/pulls/comments#create-a-reply-for-a-review-comment); `comment_id` must be a **top-level** thread root, not a reply). If mapping is uncertain, skip rather than spam the wrong thread.

Retain **thread id** next to ingested material (e.g. `<!-- gh-thread:THREAD_ROOT_ID -->` near the marker) when it helps a harness or a later run. Document that **force-pushes**, **line shifts**, and **resolved threads** can break mapping.

## Aftercare (message to user)

- Topic directory path, PR URL, and **local vs harness** outcome.
- List files changed in the commit; confirm **`comments.md`** was **not** committed.
- Top remaining **`## Q-NN`** items that still need humans.
- If harness: paste **push** and **thread-reply** handoff blocks.
