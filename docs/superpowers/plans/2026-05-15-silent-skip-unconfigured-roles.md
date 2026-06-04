# Silent Skip Unconfigured Roles — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change the `defaults.roles` gate in both dispatch workflows to silently skip (exit 0 + notice) instead of hard-fail (exit 1 + error) when a stage's role is not configured.

**Architecture:** Give the role-check step an `id` and an output flag (`skipped=true`) when the role is missing. Downstream steps (fan-out, fork-PR check) add a condition checking that flag. This avoids restructuring the workflow while preventing dispatch of unconfigured stages.

**Tech Stack:** GitHub Actions workflow YAML, Go scaffold tests

---

### Task 1: Fix per-org dispatch (`dispatch.yml` template)

**Files:**
- Modify: `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml:251-267` (role-check step)
- Modify: `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml:269-270` (fork-PR step `if`)
- Modify: `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml:288-289` (fan-out step `if`)

- [ ] **Step 1: Add `id` to role-check step and change exit behavior**

In `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml`, change lines 251-267 from:

```yaml
      - name: Check role is enabled
        if: steps.route.outputs.stage != ''
        env:
          STAGE: ${{ steps.route.outputs.stage }}
        run: |
          set -euo pipefail
          STAGE_ROLE="$STAGE"
          case "$STAGE" in
            code) STAGE_ROLE="code" ;;
            retro|prioritize) STAGE_ROLE="fullsend" ;;
          esac

          ROLES=$(yq '.defaults.roles[]' config.yaml 2>/dev/null || echo "")
          if [[ -n "$ROLES" ]] && ! echo "$ROLES" | grep -Fqx "$STAGE_ROLE"; then
            echo "::error::Stage '$STAGE' (role: $STAGE_ROLE) is not in defaults.roles — dispatch blocked"
            exit 1
          fi
```

To:

```yaml
      - name: Check role is enabled
        id: role-check
        if: steps.route.outputs.stage != ''
        env:
          STAGE: ${{ steps.route.outputs.stage }}
        run: |
          set -euo pipefail
          STAGE_ROLE="$STAGE"
          case "$STAGE" in
            code) STAGE_ROLE="code" ;;
            retro|prioritize) STAGE_ROLE="fullsend" ;;
          esac

          ROLES=$(yq '.defaults.roles[]' config.yaml 2>/dev/null || echo "")
          if [[ -n "$ROLES" ]] && ! echo "$ROLES" | grep -Fqx "$STAGE_ROLE"; then
            echo "::notice::Stage '$STAGE' skipped — role '$STAGE_ROLE' not in defaults.roles"
            echo "skipped=true" >> "${GITHUB_OUTPUT}"
            exit 0
          fi
```

- [ ] **Step 2: Add role-check guard to fork-PR and fan-out steps**

On line 270, change:
```yaml
        if: steps.route.outputs.stage == 'fix' && github.event.issue.pull_request
```
To:
```yaml
        if: steps.route.outputs.stage == 'fix' && steps.role-check.outputs.skipped != 'true' && github.event.issue.pull_request
```

On line 289, change:
```yaml
        if: steps.route.outputs.stage != ''
```
To:
```yaml
        if: steps.route.outputs.stage != '' && steps.role-check.outputs.skipped != 'true'
```

- [ ] **Step 3: Run `make lint` to validate YAML**

Run: `make lint`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml
git commit -S -s -m "fix: silent skip when role not in defaults.roles (per-org dispatch)

Change the role-check gate to exit 0 with a notice annotation instead
of exit 1 with an error when a stage's role is not configured. This
prevents noisy failed workflow runs on orgs that haven't opted into
all agents.

Fixes #973

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 2: Fix per-repo dispatch (`reusable-dispatch.yml`)

**Files:**
- Modify: `.github/workflows/reusable-dispatch.yml:234-252` (role-check step)
- Modify: `.github/workflows/reusable-dispatch.yml:51-52` (job outputs)

The per-repo dispatch uses separate jobs. The role-check step is in the `route` job, and downstream jobs gate on `needs.route.outputs.stage`. The job output on line 52 is `stage: ${{ steps.route.outputs.stage }}`. We need to override this when the role is skipped.

- [ ] **Step 1: Add `id` to role-check step and change exit behavior**

In `.github/workflows/reusable-dispatch.yml`, change lines 234-252 from:

```yaml
      - name: Check role is enabled
        if: steps.route.outputs.stage != ''
        env:
          STAGE: ${{ steps.route.outputs.stage }}
        run: |
          set -euo pipefail
          if [[ ! -f .fullsend/config.yaml ]]; then
            exit 0
          fi
          STAGE_ROLE="$STAGE"
          case "$STAGE" in
            code) STAGE_ROLE="code" ;;
            retro|prioritize) STAGE_ROLE="fullsend" ;;
          esac
          ROLES=$(yq '.roles[]' .fullsend/config.yaml 2>/dev/null || echo "")
          if [[ -n "$ROLES" ]] && ! echo "$ROLES" | grep -Fqx "$STAGE_ROLE"; then
            echo "::error::Stage '$STAGE' (role: $STAGE_ROLE) is not in configured roles — dispatch blocked"
            exit 1
          fi
```

To:

```yaml
      - name: Check role is enabled
        id: role-check
        if: steps.route.outputs.stage != ''
        env:
          STAGE: ${{ steps.route.outputs.stage }}
        run: |
          set -euo pipefail
          if [[ ! -f .fullsend/config.yaml ]]; then
            exit 0
          fi
          STAGE_ROLE="$STAGE"
          case "$STAGE" in
            code) STAGE_ROLE="code" ;;
            retro|prioritize) STAGE_ROLE="fullsend" ;;
          esac
          ROLES=$(yq '.roles[]' .fullsend/config.yaml 2>/dev/null || echo "")
          if [[ -n "$ROLES" ]] && ! echo "$ROLES" | grep -Fqx "$STAGE_ROLE"; then
            echo "::notice::Stage '$STAGE' skipped — role '$STAGE_ROLE' not in configured roles"
            echo "skipped=true" >> "${GITHUB_OUTPUT}"
            exit 0
          fi
```

- [ ] **Step 2: Gate the job `stage` output on role-check**

On line 52, change:
```yaml
      stage: ${{ steps.route.outputs.stage }}
```
To:
```yaml
      stage: ${{ steps.role-check.outputs.skipped == 'true' && '' || steps.route.outputs.stage }}
```

This clears the stage output when the role check skips, so all downstream jobs (`triage`, `code`, `review`, `fix`, `retro`) with `if: needs.route.outputs.stage == '<stage>'` are skipped.

- [ ] **Step 3: Run `make lint` to validate YAML**

Run: `make lint`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/reusable-dispatch.yml
git commit -S -s -m "fix: silent skip when role not in configured roles (per-repo dispatch)

Same change as per-org dispatch: exit 0 with notice instead of exit 1
with error when a stage's role is not configured.

Fixes #973

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

### Task 3: Update scaffold test assertions

**Files:**
- Modify: `internal/scaffold/scaffold_test.go:219-221`

- [ ] **Step 1: Check if any test assertions reference the old error message**

Run: `grep -n 'dispatch blocked\|not in defaults.roles\|not in configured roles' internal/scaffold/scaffold_test.go`

If no assertions match the old error text, no changes are needed. The existing assertion on line 221 (`assert.Contains(t, s, "defaults.roles")`) still passes because the string `defaults.roles` is still present in the notice message.

- [ ] **Step 2: Run tests**

Run: `make go-test`
Expected: PASS

- [ ] **Step 3: Run vet**

Run: `make go-vet`
Expected: PASS

- [ ] **Step 4: Commit (only if changes were needed)**

If test assertions were updated:
```bash
git add internal/scaffold/scaffold_test.go
git commit -S -s -m "test: update scaffold assertions for notice-level role skip

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>"
```
