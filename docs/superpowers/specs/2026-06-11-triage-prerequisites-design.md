# Triage Agent Prerequisites Action

**Date:** 2026-06-11
**Issue:** [#401](https://github.com/fullsend-ai/fullsend/issues/401)
**Status:** Draft

## Problem

The triage agent can detect that an issue is blocked by existing work elsewhere, but it cannot create the missing tracking issue when no such issue exists yet. A common scenario: triage evaluates a bug in a Tekton task and determines the root cause is a missing feature in an upstream container image defined in a different repo. Today the agent can only say "blocked" and point to an existing issue. If no upstream issue exists, the agent has no way to express "this needs to be filed first."

This forces humans to manually identify, draft, and file prerequisite issues in other repos before the original issue can make progress.

## Scope

This design covers **one** of three decomposition strategies identified during brainstorming:

| Strategy | Description | This design? |
|---|---|---|
| **Spin out dependency** | Original stays open + `blocked`. Agent creates upstream prerequisite issues. | Yes |
| **Split muddled issue** | Original closed. N independent successor issues replace it. | No (future work) |
| **Parent/child decompose** | Original stays open as parent. N child issues for incremental delivery. | No (future work) |

## Key discovery: cross-repo issue creation works today

A GitHub App installation token scoped to one repository can create issues in any public repo on GitHub, including repos in orgs where the app is not installed. GitHub confirmed this as a known behavior (not a vulnerability). This means the triage agent's existing token already supports cross-repo issue creation without any changes to the mint or auth infrastructure. See #402 for the original assumption that cross-installation auth would be needed.

## Design

### New `prerequisites` action

The existing `blocked` action is replaced by `prerequisites`. The triage agent's action set becomes five actions: `sufficient`, `insufficient`, `duplicate`, `question`, `prerequisites`.

The `prerequisites` action unifies two cases:
- **Existing blockers** the agent found during its search (today's `blocked` behavior)
- **New blockers** that need to be filed as issues before progress can happen

The triage result schema:

```json
{
  "action": "prerequisites",
  "prerequisites": {
    "existing": [
      { "url": "https://github.com/org/repo/issues/42" }
    ],
    "create": [
      {
        "repo": "org/upstream-lib",
        "title": "Add support for X",
        "body": "Technical description for the upstream audience..."
      }
    ]
  },
  "comment": "This issue requires upstream changes before it can proceed.",
  "label_actions": []
}
```

Constraints:
- At least one of `existing` or `create` must be non-empty.
- Both arrays can be populated in the same result (mixed existing + new blockers).
- The `blocked_by` field (singular URL, current schema) is removed.

### Hard constraint in agent prompt

> Never emit `sufficient` if unresolved prerequisites exist. Use `prerequisites` instead.

This mirrors the existing constraint: "Never emit `sufficient` with open questions."

### Agent prompt guidance for `create` entries

The agent uses its judgment on issue body content. Sometimes a back-reference to the originating issue is helpful for upstream maintainers; sometimes it leaks internal context. The agent writes the body for the upstream repo's audience, not the source repo's.

### Allowlist configuration

A new `create_issues` config field controls which repos and orgs agents are permitted to create issues in. This applies to both triage and retro agents.

```yaml
create_issues:
  allow_targets:
    orgs:
      - "my-org"
      - "upstream-org"
    repos:
      - "other-org/specific-repo"
```

Validation rules:
- If `allow_targets` is absent or empty, prerequisite creation is disabled (safe default).
- A target repo is permitted if its org appears in `orgs` OR the exact `owner/repo` appears in `repos`.
- The source repo (where triage is running) is always implicitly allowed.
- Entries in `repos` must be `owner/name` format. Empty strings are rejected.

### Install-time defaults

The admin setup flow populates `create_issues.allow_targets` with sensible defaults:

- **Org mode:** `allow_targets.orgs` includes the org. `allow_targets.repos` includes `fullsend-ai/fullsend`.
- **Per-repo mode:** `allow_targets.repos` includes the target repo and `fullsend-ai/fullsend`.

### Post-script behavior

When the post-script receives `action: "prerequisites"`:

1. **Process `create` entries:** For each entry, validate `repo` against `create_issues.allow_targets`. If allowed, create the issue using existing `forge.Client.CreateIssue` plumbing. Collect the resulting URL. If disallowed or the API call fails, record the failure.

2. **Merge URLs:** Combine URLs from successfully created issues with the `existing` array to produce the full blocker list.

3. **Apply labels:** Remove `ready-to-code` and `needs-info`. Add `blocked` label. (Same as current `blocked` action behavior.)

4. **Post comment:** Sticky comment (via `fullsend post-comment`) summarizing the prerequisites. Links to all blockers (existing and newly created). For entries that could not be filed (allowlist rejection or API failure), include the agent's draft in a collapsed section so a human can file it manually:

   ```html
   <details>
   <summary>Prerequisite: org_a/repo -- Add support for X</summary>

   [the full body the agent drafted for the upstream issue]

   </details>
   ```

5. **Partial success:** If some creates succeed and others fail, the issue still gets `blocked` with whatever blockers were established. The comment notes which prerequisites could not be created and why.

The existing `blocked` action handler in the post-script is removed. `prerequisites` fully replaces it.

### Re-triage flow

When a prerequisite issue is resolved and the original issue is re-triaged, the agent discovers blocker URLs from the sticky comment posted by the post-script (which contains links to all prerequisite issues). The existing blocker-checking logic in the agent prompt (Step 2) already inspects linked issues and checks their state. If all prerequisites are resolved, the agent can emit `sufficient` or another appropriate action. No changes needed to the re-triage flow.

## Changes required

| Component | File | Change |
|---|---|---|
| Config structs | `internal/config/config.go` | Add `CreateIssues` struct with `AllowTargets` (Orgs `[]string`, Repos `[]string`) to both `OrgConfig` and `PerRepoConfig`. Update constructors with install-time defaults. Add validation. |
| Triage result schema | `internal/scaffold/fullsend-repo/schemas/triage-result.schema.json` | Replace `blocked` with `prerequisites` in action enum. Add `prerequisites` object schema. Remove `blocked_by`. |
| Agent prompt | `internal/scaffold/fullsend-repo/agents/triage.md` | Replace `blocked` action with `prerequisites`. Add hard constraint. Add guidance for `create` entry content. |
| Post-script | `internal/scaffold/fullsend-repo/scripts/post-triage.sh` | Replace `blocked` handler with `prerequisites` handler. Add allowlist validation, issue creation, degraded path with collapsed draft. |
| Pre-script | `internal/scaffold/fullsend-repo/scripts/pre-triage.sh` | No change. `blocked` label stripping stays the same. |
| User docs | `docs/agents/triage.md` | New section documenting `create_issues` config surface: what it does, defaults, when to expand or restrict. |
| Config constructors | `internal/config/config.go` | `NewOrgConfig` and `NewPerRepoConfig` populate `create_issues.allow_targets` defaults. Callers in `internal/cli/admin.go` and `internal/cli/github.go` pass the org/repo context. |

## Out of scope

- **Split muddled issues** (close original, create N independent successors)
- **Parent/child decomposition** (original stays open, create N children)
- **Cross-repo issue editing** (GitHub enforces scope on edits, only creation bypasses it)
- **Retro agent integration** (uses the same `create_issues` config, but prompt/post-script changes are separate work)
