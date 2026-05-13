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
- **What is the severity?** Workaround available? Blocks other work?
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

### 4. Assess issue structure

Determine whether the user's request requires a single issue or multiple linked
issues. Signals that indicate multiple issues:

- Multiple distinct deliverables (e.g. an ADR, a feature implementation, and an
  experiment).
- Work spanning multiple repositories.
- A natural parent that decomposes into independent sub-tasks.

If any of these apply, ask the user whether they want a parent issue with
sub-issues. If they confirm, proceed to **"Multi-issue hierarchies"** below
instead of steps 5–6.

If the request is a single issue, continue with step 5.

### 5. Write the issue

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

### 6. File the issue

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

## Multi-issue hierarchies

When step 4 determines the request needs multiple linked issues, use GitHub
sub-issues to create a parent/child hierarchy.

### Plan the hierarchy

Before filing anything, present the full structure to the user:

- Which issue is the parent (tracking issue).
- Each sub-issue: its target repo, title, and a one-line description.

GitHub allows a maximum of 100 sub-issues per parent and 8 levels of nesting.

Wait for the user to approve the hierarchy before filing.

### File in order

1. **Create the parent issue first.** It has no dependencies. Its body should
   describe the overall goal — the sub-issues provide the breakdown.

2. **Get the parent's node ID:**

```bash
gh api graphql -H "GraphQL-Features: sub_issues" -f query='
  query {
    repository(owner: "<owner>", name: "<repo>") {
      issue(number: <N>) { id }
    }
  }
' --jq '.data.repository.issue.id'
```

3. **For each sub-issue**, create it and immediately link it to the parent:

```bash
# Create the sub-issue (may target a different repo)
gh issue create --repo <owner/name> \
  --title "<title>" \
  --body "$(cat <<'EOF'
Tracking issue: <owner/repo>#<parent-number>

<body>
EOF
)"

# Get the sub-issue's node ID
CHILD_ID=$(gh api graphql -H "GraphQL-Features: sub_issues" -f query='
  query {
    repository(owner: "<owner>", name: "<repo>") {
      issue(number: <N>) { id }
    }
  }
' --jq '.data.repository.issue.id')

# Link as sub-issue (use GraphQL variables to avoid shell quoting issues)
gh api graphql -H "GraphQL-Features: sub_issues" \
  -f parentId="<parent-node-id>" \
  -f childId="$CHILD_ID" \
  -f query='mutation($parentId: ID!, $childId: ID!) {
    addSubIssue(input: { issueId: $parentId, subIssueId: $childId }) {
      issue { title }
      subIssue { title }
    }
  }'
```

If the `addSubIssue` mutation fails (permissions, feature not enabled, rate
limit), report the error and the URLs of all issues created so far to the user.

### Cross-repo sub-issues

GitHub sub-issues support cross-repo linking within the same organization (or
user account). Cross-organization linking is not supported. When a sub-issue
targets a different repository:

- Use `--repo <owner/name>` on the `gh issue create` call to target the correct
  repo.
- Include `Tracking issue: <owner/repo>#<number>` in the sub-issue body so the
  relationship is visible even outside the GitHub sub-issues UI.

### Report

Return all issue URLs to the user, grouped by parent and sub-issues.

## Constraints

- **Never file without user approval.** Always present the draft and wait.
- **Never propose solutions.** The issue describes the problem. Period.
- **Never invent facts.** If you lack information, ask. Do not guess at version
  numbers, error messages, or reproduction steps.
- **Respect the repo's conventions.** If existing issues use a template or
  follow a pattern, match it. Check `.github/ISSUE_TEMPLATE/` if it exists.
- **Use sub-issues for hierarchies.** When filing multiple linked issues, create
  the parent first and link sub-issues via the GraphQL API. Do not rely on
  markdown task lists as the tracking mechanism.