# Commit Hygiene

How do we keep the commit history readable when review rounds produce a stream of "address PR feedback" commits?

**Related:**
- [contributor-guidance.md](contributor-guidance.md) — teaching the rules to humans and agents
- [flapping-convergence.md](flapping-convergence.md) — review ping-pong pollutes PR histories
- [human-factors.md](human-factors.md) — commit history as a record of judgment
- [code-review.md](code-review.md) — the review loop that produces the commits

## Why this matters

A typical PR goes through several review rounds. Each round produces commits
like "address PR feedback", "apply review suggestions", "fix typo", or
"revert previous approach". Agents make this worse because they push a new
commit for every review comment, without merging the changes into a coherent
story.

The result is a history where the subject lines describe the *review process*
instead of the *change*. Someone reading `git log` six months later sees ten
commits that say "address PR feedback" and learns nothing. Bisecting a
regression lands on "fix typo" with no hint of what it fixed or why.

The numbers confirm this. In the three months to 2026-11-06, `main` received
4,947 commits. 264 of them (about 5%) say "address review feedback". They
spread over 159 distinct PRs, and some PRs collected three such commits on
their own. The `fullsend-fix` bot authored 252 of the 264, so this is mostly
an agent pattern, not a human one.

Merge commits add a second layer of noise. In the same three months, `main`
received 1,356 merge commits (27% of all commits): 1,147 "Merge pull
request" commits from the merge queue, and 189 "Merge branch 'main' into …"
commits from contributors syncing their branches. A reader must skip two
lines to find one real commit.

This problem used to leak into release notes, because GoReleaser built them
from commit subjects. It still does until
[ADR 0160](../ADRs/0160-gather-release-notes-from-pr-bodies.md) lands. The
past releases show the damage: v0.49.0 listed 26 "address review feedback"
entries out of 145 (18%), and v0.54.0 listed 7 out of 61. Users reading those
notes learned nothing about the product. ADR 0160 removes that leak by taking
release notes from `release-note` blocks in PR bodies. But the history itself
stays noisy, and humans still read it.

## The problem in detail

Today, PRs land through the merge queue as merge commits. The branch commits
arrive on `main` as they were pushed. Nothing collapses them. So the history
records every review round as separate commits, wrapped in merge commits.

GoReleaser excludes merge commits from the release notes (the changelog
filter drops subjects that start with "Merge"), so users do not see this
layer. But `git log` readers do, and every merge commit doubles the length of
the history they must scan.

There is a tension:

- **During review**, granular commits are useful. The reviewer can see what
  changed since the last look. "Address PR feedback" is an honest label for
  "this is the delta you asked for".
- **After merge**, those same commits are noise. The review conversation lives
  in the PR, not in the history. The history should carry the change, told as
  a small number of well-formed commits.

Any fix must serve both human and agent contributors. Agents can rebase
mechanically, but only if the project tells them to, because agents follow
written rules rather than absorbed norms (see
[contributor-guidance.md](contributor-guidance.md)).

## Options

### 1. Squash on merge

The merge queue squashes every PR into a single commit. The PR title becomes
the commit subject.

- **Pro:** No feedback commits ever reach `main`. Zero contributor effort.
- **Con:** Large PRs become one opaque commit. Authors who did write a clean
  series lose it. The PR title must carry the full Conventional Commits
  discipline on its own.
- **Con:** DCO sign-off collapses to the PR author, which changes how
  multi-author work is recorded.

### 2. Require a clean series before merge

Contributors rebase their branch before merge: fold feedback commits into the
commits they belong to (`git rebase -i --autosquash`), and keep a small,
readable series. CI or a bot can check for feedback-shaped subjects and ask
for a rebase.

- **Pro:** The history keeps meaningful, reviewable commits. Bisect works.
- **Con:** It is work, and it is easy to forget. Humans must do it manually;
  agents need an explicit written rule.
- **Con:** Rewriting history after review can hide what the reviewer actually
  approved, unless the final diff is identical.

### 3. Bot-driven cleanup before merge

A bot rewrites the branch automatically before merge: it squashes commits
whose subjects match feedback patterns into their target commits, or collapses
the whole branch when no structure is detectable.

- **Pro:** Contributors do nothing. The history is clean.
- **Con:** A bot that rewrites history can make mistakes no human would. It
  also needs a reliable way to tell a feedback commit from a real one.

### 4. Status quo

Keep merge commits and per-round commits. Rely on ADR 0160 so users never see
the noise, and accept that `git log` is for archaeologists.

- **Pro:** Nothing to build or enforce.
- **Con:** The history keeps degrading as agent-authored PRs grow.

## Open questions

- Do we value per-PR commit series at all, or is one commit per PR enough?
- Can a subject-based heuristic ("address PR feedback", "apply suggestions",
  "fix typo") reliably spot feedback commits, or will agents simply write
  better subjects?
- Should the rule differ for human and agent contributors, or must it be one
  written rule for both?
- Does the merge queue support squash or rebase merge modes with the checks we
  require today?
- Branch-sync merges ("Merge branch 'main' into …") are avoidable: a
  contributor can rebase instead. Should we ask for that, or is the merge
  queue's merge commit per PR the only merge noise worth removing?
