---
name: Filing GitHub Issues
description: >
  File well-crafted GitHub issues. Use when the user wants to report a bug,
  request a feature, propose a change, or file any GitHub issue. Searches for
  duplicates, asks clarifying questions, and creates the issue using the
  gh CLI.
---

# Filing GitHub Issues

A good issue gives a reader everything they need to understand the problem
without prescribing a solution. It states what is wrong or what is missing,
why it matters, and how to observe it. The reader should finish with a clear
picture of the problem and enough context to investigate independently.

## Process

Follow these steps in order. Do not skip steps.

### 1. Identify the target repository

Determine which repository should receive this issue:

- If the user specifies a repo, use it.
- If the current working directory is a git repo, default to its `origin` remote.
- If neither applies, ask.

Run `gh repo view` to confirm you have access and note the repo's full `owner/name`.

### 2. Search for existing issues

Before writing anything, search for duplicates and related issues:

```bash
gh issue list --repo <owner/name> --state all --search "<key terms>"
```

Try at least two different search queries using different terms from the user's
description. Search broadly — use core nouns and verbs, not the user's exact
phrasing.

**If you find related issues:**

- Present them to the user with issue number, title, and a one-line summary.
- Ask whether any of these captures their intent, whether the new issue should
  reference them, or whether to proceed with a new issue.
- Do not file a duplicate without the user's explicit go-ahead.

### 3. Ask clarifying questions

Think divergently about what this issue needs before you write it. Consider
the problem from multiple angles:

- **Who is affected?** End users, developers, CI systems, downstream consumers?
- **What triggers it?** Specific actions, configurations, timing, data shapes?
- **Where does it manifest?** Which component, service, environment, platform?
- **When did it start?** Always been this way, or a regression? What changed?
- **What is the impact?** Workaround available? Blocks other work?
- **What is the scope?** Isolated incident or pattern? How many people hit this?
- **What has been tried?** Prior debugging, workarounds, related PRs?
- **What context would a stranger need?** Version numbers, error messages, logs,
  screenshots, links to related discussions?

From these angles, identify the gaps — what the user hasn't told you but a
reader would need. Then ask your clarifying questions:

- Ask only questions whose answers would materially change the issue. Skip
  anything you can fill in yourself from context.
- Prefer multiple-choice or yes/no questions over open-ended ones.
- Ask all your questions in a single message, grouped logically.
- Three to five questions is typical. Fewer is fine. More than seven means
  you should narrow your focus.

Wait for the user's answers before proceeding.

### 4. Write the issue

Draft the issue title and body.

**Title:** A concise phrase that a reader can scan in a list and understand
without opening the issue. Lead with the component or area if the repo uses
that convention. Avoid vague words like "issue with" or "problem in."

**Body structure — use only the sections that apply:**

- **What happens:** The current behavior, stated as fact. Include error messages,
  symptoms, or the observable gap.
- **What should happen:** The expected or desired behavior. Be specific enough
  that someone could verify a fix against this description.
- **How to reproduce:** Numbered steps, starting from a clean state. Include
  the environment, version, and configuration that matter. Omit this section
  for feature requests or design issues.
- **Context:** Why this matters. Who it affects. What prompted this report.
  Links to related issues, discussions, or documentation.

**What to leave out:**

- Do not propose a solution in the issue body. The issue captures the problem;
  solutions belong in follow-up discussion or linked PRs.
- Do not pad the issue with generic text, boilerplate, or pleasantries.
- Do not add sections with no content. If you have nothing for "How to
  reproduce," omit the section entirely.

Present the draft to the user. Wait for approval or edits before filing.

### 5. File the issue

After the user approves the draft:

```bash
gh issue create --repo <owner/name> \
  --title "<title>" \
  --body "$(cat <<'EOF'
<body>
EOF
)"
```

Return the issue URL to the user.

#### Sub-issues (parent / child in GitHub)

When the user wants a **child issue under a parent** (epic, breakdown, hierarchy),
GitHub’s **sub-issue relationship is not created by the issue body**. Text like
“Part of #N” or cross-links is optional for readers only; the graph stays flat
without an API call.

After creating the parent and child (parent first, then child), link them with
the **REST sub-issues API**:

1. Resolve the child’s **database `id`** (integer), which is **not** the issue
   `number`, and assign it (example: `CHILD_ID=$(gh api ... --jq .id)`):
   ```bash
   CHILD_ID=$(gh api repos/<owner>/<name>/issues/<child_number> --jq .id)
   ```
2. Attach the child to the parent:
   ```bash
   echo "{\"sub_issue_id\": $CHILD_ID}" | gh api repos/<owner>/<name>/issues/<parent_number>/sub_issues \
     --method POST --input -
   ```
   Use `"replace_parent": true` in the JSON body only when moving a child that
   already has a parent.
3. Confirm:
   ```bash
   gh api repos/<owner>/<name>/issues/<child_number>/parent
   ```

**GraphQL (alternative):** `addSubIssue` with parent and child node IDs, or
`createIssue` with `parentIssueId` when the child is created in the same step.

Do this **after** each `gh issue create` succeeds; then return all issue URLs.

## Constraints

- **Never file without user approval.** Always present the draft and wait.
- **Never propose solutions.** The issue describes the problem. Period.
- **Never invent facts.** If you lack information, ask. Do not guess at version
  numbers, error messages, or reproduction steps.
- **Respect the repo's conventions.** If existing issues use a template or
  follow a pattern, match it. Check `.github/ISSUE_TEMPLATE/` if it exists.
- **No labels at filing time.** Never include `--label` flags in
  `gh issue create`. Labels are applied by the triage pipeline, not at
  filing time.
- **No priority classification.** Do not ask the user for priority or
  severity levels, and do not include priority labels. Triage handles
  classification.
- **Sub-issues:** If there is a parent/child hierarchy, use GitHub’s sub-issue
  API after creation; do not rely on body mentions alone.
