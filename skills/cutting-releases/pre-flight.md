# Pre-Flight Release Check

Part of the [cutting-releases](SKILL.md) skill.

Run this audit **before** tagging. The goal is to verify that moving
the `v0` reusable-workflow tag will not break downstream consumers,
and to identify what needs post-flight verification.

Start by fetching the latest remote state:

```
git fetch origin
```

## A. Audit reusable workflow changes

```
git diff v0..origin/main -- .github/workflows/reusable-*.yml
```

For each changed workflow, read the full diff and check:

- **Inputs:** Were any inputs removed or renamed? Were required inputs
  added without defaults? These are breaking — callers will fail.
- **Outputs:** Were any job outputs removed or renamed? Callers that
  reference them will break.
- **Secrets:** Were new secrets added to `secrets:` blocks? Callers
  must already have those secrets or the workflow will fail silently.
- **Environment variables:** New env vars passed to steps are additive
  and safe. Changed env var names used in conditionals may alter
  behavior.
- **Job/step IDs:** Renamed job IDs break `needs:` references in
  caller workflows.
- **Permissions:** Changes to `permissions:` blocks may fail if the
  calling workflow's token doesn't grant the new scopes.

Classify each change as:
- **Additive** (new optional inputs, new env vars) — safe.
- **Default change** (different default values) — note for migration.
- **Breaking** (removed/renamed inputs, outputs, jobs, new required
  secrets) — block the release until resolved.

## B. Audit scaffold and template changes

```
git diff v0..origin/main -- internal/scaffold/fullsend-repo/
```

Scaffold files are deployed at `github setup` time, not consumed live
via `@v0`. Changes here affect **new installs and re-scaffolds only**.
Review for:

- **Agent definitions** (`agents/`): Changed models, tools, or
  instructions alter agent behavior on next scaffold.
- **Harness configs** (`harness/`): Changed resource limits, allowed
  tools, or validation rules.
- **Hook scripts** (`scripts/`): Changed pre/post hooks run inside
  agent sandboxes.
- **Skill files** (`skills/`): New or changed agent skills.
- **Workflow templates** (`.github/workflows/`): Templates that get
  copied into target repos at scaffold time.

These do not require post-flight verification against running systems,
but note significant behavior changes for the release summary.

## C. Audit CLI and function changes

```
git log --oneline v0..origin/main -- cmd/ internal/
```

For commits touching `cmd/` or `internal/cli/`, read the diffs and
check:

- **Renamed flags or sub-commands:** Deprecated aliases must be
  preserved via `MarkDeprecated` + `MarkHidden`. If a flag was
  removed without an alias, this is breaking.
- **Changed defaults:** Pool names, regions, WIF provider names, or
  project ID defaults that differ from the previous release require
  a migration note in the release summary.
- **New sub-commands or flags:** Additive, safe. Note for changelog.
- **Behavioral changes in `internal/`:** Read the changed functions
  to understand if existing workflows (mint enroll/unenroll, inference
  provision, app setup) behave differently. Check backward compat by
  verifying the old invocation still works.

## D. Check CI on main

```
gh run list --branch=main --limit=5
```

All recent runs should be passing. If E2E tests are failing, investigate
before releasing.

## E. Identify post-flight check areas

Based on the changes found in steps A–C, determine what needs
post-flight verification after the `v0` tag moves:

- **Reusable workflow changes** → verify workflow runs in fullsend-ai
  repos resolve `@v0` correctly and pass.
- **New secrets or permissions** → verify affected workflows don't
  fail on missing secrets.
- **CLI default changes** → note migration steps for existing
  installs in the release summary.
- **No reusable workflow changes** → post-flight can be limited to
  confirming the release artifacts built correctly.

## F. Present summary

Summarize findings to the user in a table:

| Area | Changes | Breaking? |
|------|---------|-----------|
| Reusable workflows | ... | No/Yes |
| Scaffold templates | ... | No/Yes |
| CLI / internal | ... | No/Yes |

List the post-flight check areas identified in step E.

Give a **GO / NO-GO** verdict. Do not proceed until the user confirms.
