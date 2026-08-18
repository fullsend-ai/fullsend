---
name: user-forum-whats-new
description: >
  Use when preparing the Fullsend user forum "What's New" agenda, a
  Tuesday-to-Tuesday recap, forum-host notes for the standing Google Doc,
  or copy-paste HTML of shipped changes for users. Also use when the
  user says what's new in Fullsend, user forum bullets, or forum agenda.
allowed-tools: Read, Write, Grep, Glob, WebFetch, Bash(bash skills/user-forum-whats-new/scripts/gather.sh:*), Bash(python3 skills/user-forum-whats-new/scripts/gather.py:*), Bash(gh api:*), Bash(gh search:*), Bash(gh issue:*), Bash(gh pr:*), Bash(gh release:*), Bash(xdg-open:*), Bash(open:*)
---

# User forum What's New

Build a **talkable** weekly recap for the Fullsend user forum. Audience
already saw (or can open) GitHub release notes. This recap is the 4–6
minute verbal pass: what matters, with a click that shows the change.

**Core rule:** every bullet has a clickable example of the actual change.
No example → do not ship the bullet. The example **may be the code that
landed** (file + line range or a focused PR diff). Live artifacts are
better when they exist; code is a valid fallback, not a last resort you
skip.

## Where this lives

Canonical directory (this skill):

`skills/user-forum-whats-new/` in the `fullsend-ai/fullsend` repo.

Anyone with a checkout already has it: `.cursor/skills` and
`.claude/skills` in this repo symlink to `skills/`. Invoke by asking
for the user-forum What's New recap, or `@skills/user-forum-whats-new`.

To use it from other workspaces:

```bash
ln -s /path/to/fullsend/skills/user-forum-whats-new ~/.cursor/skills/user-forum-whats-new
```

Needs `gh` (authenticated) and `python3`.

## When

Tuesday morning before the forum (or when the host asks). Window is
**after the previous Tuesday forum through this Tuesday**.

Standing agenda:
[Notes — fullsend community meeting](https://docs.google.com/document/d/1kXRvb7QJIlv4MoSnTzIM5uthURckhN0Duy8JFar5roQ)

Local copy of past What's New (optional, if the host keeps one):
use it only to confirm the last forum date. The standing Google Doc is
canonical.

## Workflow

Do these in order. Do not draft bullets until ranking is done.

### 1. Resolve the window

- Last forum date = previous Tuesday (confirm from the notes file).
- `SINCE` = that meeting's start (America/New_York 08:00 is fine).
- `UNTIL` = this Tuesday forum (now, if running that morning).
- `gather.sh` / `gather.py` interpret `--since` as **08:00 America/New_York**
  on that date and `--until` as end of that day ET (or **now** when until is
  today). Default `--until` is today's date in America/New_York (not the
  host machine's local calendar).
- Search and filter use the same UTC timestamp bounds (not bare calendar
  dates), so ET evening after UTC midnight is not dropped.
- Releases published **after** the last forum are in-scope even if they
  share the calendar day (e.g. v0.36.0 shipped the afternoon of Aug 11).

### 2. Gather candidates

From the repository root:

```bash
bash skills/user-forum-whats-new/scripts/gather.sh --since YYYY-MM-DD --until YYYY-MM-DD
```

That prints JSON: releases (full changelog body), merged PRs split into
`merged_prs.released` and `merged_prs.on_main`, plus `window_start_utc`,
`window_end_utc`, and `release_cutoff_utc` (per-repo map of latest
in-window release publish times — each PR is classified against **its own
repo's** cutoff). Unit tests: `python3 skills/user-forum-whats-new/scripts/gather_test.py`.

**Score Features from the release body even when their PRs merged
before `SINCE`.** The gather window only lists PRs merged this week;
the release that shipped after last Tuesday still counts.

Also scan:

- `#forum-fullsend-ai` announcements of things that **landed this window**
  (a new dashboard, a new knob). That is the user-forum channel; it is
  distinct from `#forum-konflux-fullsend` (release announcements in
  `.goreleaser.yml`). Ignore “here is a team using X” when X has been
  available for weeks.
- Docs/guide sections that landed in the window
- Live comments / dashboards / runs that **show a change from this window**

Do **not** treat the GitHub release body as the recap. Use it as a
candidate list only.

### 3. Attach one example per candidate

For each candidate, find **one** click in this preference order:

| Rank | Example type | What to link |
|------|----------------|--------------|
| A | Live artifact | Issue/PR comment, dashboard, Actions run, config in a real repo |
| B | Landing code | `https://github.com/fullsend-ai/<repo>/blob/<sha>/<path>#L<start>-L<end>` **or** a PR `files` URL that opens on the changed hunk. Tag refs (`v0.36.0`) are fine for released items. |
| C | How-to surface | Docs **section** that *is* the new UI (heading anchor), not a changelog |

**Code is enough.** A harness `gitlab:` block, a YAML knob, or the
function that posts a new comment is a valid example. Prefer the
hunk that *did the work*, not a search for a live demo that may not
exist yet.

Disqualify if you cannot produce A, B, or C.

**Never** use these as the example:

- The release notes page (`…/releases/tag/vX.Y.Z`)
- The PR conversation tab with no file/hunk focus
- An issue that requested the work (unless the issue *is* the demo)

You may mention the version in prose (`v0.36.0`).

### 4. Score (ranking)

**Newness filter (before scoring):** the *capability* must have shipped
this window (released after last Tuesday, or merged to main this week),
or be a user-facing surface that did not exist before (new dashboard
built this week). A live run, comment, or “working with a team” demo of
something they have had for weeks is **not** What's New — drop it even
if the click is excellent. Check the doc/PR/tag date, not how recently
you found an example. Tracing in July with a Quay run this week fails
this filter.

**Experience filter (before scoring):** would a typical person in the
room *notice a different outcome* or *do something differently* this
week? If no — it is platform-important, not What's New. Do not give it
+3 “custom-agent authors” or +3 “new capability they can use.” Cap at
audience +1 and omit it. Examples: sandbox env plumbing
(`FULLSEND_ROLE` / `FULLSEND_SLUG`), internal Go types, CLI internals
that do not change how agents are run or read.

**Talkable filter (before scoring):** can the host say in one sentence
what *they* will see or do — not “there was a bug, you might have been
hitting it”? If users may or may not be affected and there is nothing
to show but a code comment, **drop**. Silent correctness for a maybe-
affected subset (Jira group pagination past member 100, similar) stays
in release notes. If the host cannot explain the bullet without
opening the patch, **drop**. Installer YAML plumbing (`mint_url` /
`inference:` in `config.yaml`, scalar-override docs) is release notes
unless the room will actually edit that config this week.

Four bands. **Pick exactly one value from each band.** Max 15.

```
Audience (one):
  +5  all (or most) installs will notice this week
  +3  a large subset (GitLab, Jira, people who author/tune agents)
  +1  admins / mint / SRE / skill-plumbing only

Change (one):
  +4  default behavior change (no opt-in)
  +4  action required (upgrade, breaking flag, security pin)
  +3  new capability they will actually use this week
      (set a knob, click a dashboard, see a new comment — not “could
      read an env var if they wrote a skill”)

Ship (one):
  +3  released in this window
  +1  on main / next release only
      (not “community share of an older feature”)

Example (one):
  +3  live artifact (A)
  +2  landing code (B)
  +1  how-to docs section (C)
```

Talk the highest scores first.

Tie-break: broader audience, then released, then easier to click in 20
seconds.

**Drop** even a high conceptual score if the example is weak (release
notes, vague PR).

**Best of the best only** within each section below. Everything that
survives the filters goes in **Released** or **On main** — not a single
flat list and not an "Also landed" dump. If only two items qualify for
a section, ship two; do not pad with release-note filler.

### 5. Split into Released vs On main

Every kept bullet belongs in exactly one section.

| Section | What goes here |
|---------|----------------|
| **Released** | In the newest release published this window (`v0.36.0`, etc.). Also: external surfaces users can **use right now** — a live dashboard, a docs page, a skill on `main` they can invoke today. If it works when they click it, treat it as released even without a tag. |
| **On main (next release)** | Merged to `fullsend` or `agents` `main` **after** that release was tagged. Link landing code on `main` (not the release tag). |

`gather.py` splits merged PRs using each repo's latest in-window release
`published_at` as that repo's cutoff (`release_cutoff_utc` is a per-repo
map in JSON). You still assign bullets manually when the source is a
release-body feature (PR merged before the window) or a live external
surface.

Talk **Released** first, then **On main**.

### 6. Write HTML for Google Docs

Write `/tmp/fullsend-whats-new-YYYY-MM-DD.html` (this Tuesday's date).

Constraints:

- Simple HTML: `h2` date, `h3` What's new in Fullsend, then two subsections:
  - `h4` **Released** — bullets for shipped / usable-now items
  - `h4` **On main (next release)** — bullets for post-release merges
- Arial 11pt, no fancy CSS (Google Docs paste)
- Each bullet: **bold hook** + one spoken sentence + the example link
  (and a second link only if it is the knob/docs they need)
- No "Versions Released:" bullet whose only links are release tags
- No third "Also landed" section — leftovers stay in GitHub release notes
- Open the file (`xdg-open` on Linux, `open` on macOS) so the host can
  Select All → Copy → Paste

### 7. Return to the host

In chat, include:

1. Skill path (this directory)
2. Window used
3. A score table (candidate, score, bucket Released/On main, example type A/B/C, keep/drop)
4. Path of the HTML file
5. Talking order — **Released** bullets first, then **On main**

Do not edit the Google Doc unless the host asks.

## Audience (who is in the room)

Custom-agent authors, Konflux, RHDH, Quay, GitLab-waiters, Jira users.
Optimize for **what they will see or can start using**, not what the
core team shipped internally.

Usually **drop** unless score still clears the floor with a code example:

- Mint Cloudflare/GCP operator flags
- Embed-sync / test-only refactors
- SHA-pinning upgrade internals
- Dependency bumps
- Defaults that were reverted in a later commit this window
- Sandbox/env plumbing that skills *could* read but users will not
  notice (`FULLSEND_ROLE`, `FULLSEND_SLUG`, similar)
- Team-working demos of capabilities that shipped in prior weeks
  (a tracing run, a “look they turned it on” screenshot)
- Silent bugfixes users may or may not hit, with nothing to show but
  the patch (Jira per-actor role lookup / group pagination cap)
- Installer / config-layer plumbing the host cannot explain in one
  spoken sentence (`mint_url` and nested `inference:` in config.yaml)

## Rationalizations (do not)

| Excuse | Reality |
|--------|---------|
| "Users already have release notes, so 4 bullets is enough" | Release notes ≠ talk track. Share the best 5–8, not a leftover dump. |
| "No live comment, skip it" | Link the code that landed. |
| "Link the release tag, they can drill in" | They already can. That is not this recap. |
| "On-main isn't released, omit it" | Put it under **On main (next release)** — do not skip it. |
| "It's only on main, not released" | That is the **On main** section, not a reason to drop it. |
| "Dashboard/skill is not in a tag" | If users can click and use it today, put it under **Released**. |
| "This is important but I can't show it" | Then it is not a What's New bullet. |
| "Custom-skill authors might use this env/API" | If it does not change how they run agents or what they see, it is not a talk-track bullet. |
| "Here is a real run you can click" | If they have had that capability for weeks, it is hallway info, not What's New. |
| "Put the rest in Also landed so we don't miss anything" | They have release notes for that. The recap is only the best of the best. |
| "It's a real bug we fixed" | If the host cannot say more than “you might have been hitting this,” it is not talk-track. |
| "It shipped in config.yaml this release" | If the host does not understand it, the room will not either. Drop it. |

## Red flags — rewrite before paste

- Any `releases/tag/` URL used as the example
- An **Also landed** leftover dump at the end
- A single flat bullet list with no **Released** / **On main** split
- A bullet whose click does not show the change in <20 seconds
- Mint-delete / Cloudflare PEM as a top item
- "Interrupted" / "Terminated" status comments that do not say *why*
  (prefer follow-up failure comments that name the cause, or the code
  that emits them)
- A live artifact whose underlying feature predates this window
  (tracing, old dashboards, last month's knobs)
