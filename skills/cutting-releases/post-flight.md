# Post-Flight Verification

Part of the [cutting-releases](SKILL.md) skill.

Run after the version tag is pushed, the `v0` tag is moved, and the
CI workflows complete. Focus on the areas identified during pre-flight
step E.

## A. Wait for CI workflows

Wait for the Release workflow (triggered by the `v*` tag) and the
Sandbox Images workflow (triggered by the `v0` tag move) to complete:

```
gh run list --workflow=release.yml --limit=1
gh run list --workflow=sandbox-images.yml --limit=1
```

Both must pass before proceeding. If either fails, investigate and
resolve before continuing — a broken release or sandbox image affects
all downstream consumers.

## B. Verify the release artifacts

```
gh release view <tag>
```

Check that the title, changelog, and binary assets look correct.
Verify the release is not marked as a draft.

## C. Check fullsend-ai repos

The skill user is a fullsend repo admin, so fullsend-ai org repos
are always accessible. Check recent workflow runs in the org's repos
that consume `@v0` reusable workflows:

```
gh run list --repo fullsend-ai/fullsend --limit=3
gh run list --repo fullsend-ai/.fullsend --limit=3
```

Look for runs that started **after** the `v0` tag move. Confirm they
completed without workflow-resolution errors (e.g. "could not find
reusable workflow"). If no runs occurred naturally, check for any
recent failed or cancelled runs that can be retriggered:

```
gh run list --repo fullsend-ai/.fullsend --status=failure --limit=3
```

Present any candidate to the user for confirmation before retriggering:

> I found run `<run-id>` (failed) in `fullsend-ai/.fullsend`.
> Retrigger it to verify `@v0` resolves?

Once confirmed:

```
gh run rerun <run-id> --failed --repo fullsend-ai/.fullsend
```

## D. Check additional downstream repos (optional)

Use `AskUserQuestion` to ask if the user has access to additional
downstream orgs:

> Do you have access to any other downstream orgs/repos to verify?
> (e.g. "konflux-ci, redhat-developer/rhdh-agentic")
> Leave blank to skip.

If the user provides repos, repeat the same checks from step C for
each one. If blank, skip this step — not all admins have access to
every enrolled org.

## E. Present post-flight summary

Summarize results to the user:

| Org/Repo | `@v0` Refs | Status |
|----------|-----------|--------|
| fullsend-ai/.fullsend | Confirmed | Passing |
| ... | ... | ... |

Distinguish between:
- **Release-related failures** — workflow resolution errors, missing
  secrets, or permission failures caused by the tag move.
- **Unrelated failures** — agent runtime errors, external API issues,
  or pre-existing test failures.
