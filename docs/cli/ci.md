---
sidebar_label: fullsend ci
---

# fullsend ci

CI helper commands. These run inside GitHub Actions. They are not
interactive operator tools.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend ci check-e2e-authorization` | Decide whether a pull request may run credentialed e2e tests |

## `ci check-e2e-authorization`

Decide whether a pull request may run credentialed e2e tests.

**Same-repository `pull_request` runs** (fullsend `e2e.yml`): authorize when
the PR author is a trusted bot or the collaborator permission API confirms
write access or higher.

**Fork `/ok-to-test` runs** (`EVENT_NAME=issue_comment`, `e2e-ok-to-test.yml`):
fetch the comment by `COMMENT_ID`, verify it belongs to the PR, require the
commenter to have write+ access, and authorize only when the first line is
`/ok-to-test` followed by the full 40-character PR head sha.

**Labeled events** (for example the agents repository gate): authorize only
when `EVENT_LABEL` is `ok-to-test` and the labeler has write+ access.
fullsend's own `e2e.yml` no longer triggers on `labeled`
([#9245](https://github.com/fullsend-ai/fullsend/issues/9245)).

This is the Go replacement for
[`scripts/check-e2e-authorization.sh`](../../scripts/check-e2e-authorization.sh).

> **Planned:** Wire
> [`.github/actions/check-e2e-authorization`](../../.github/actions/check-e2e-authorization/action.yml)
> to this CLI instead of the bash script ([#8371](https://github.com/fullsend-ai/fullsend/issues/8371) part 3).

For the policy itself, see [E2E testing](../guides/dev/e2e-testing.md)
and [CI workflows](../contributing/ci-workflows.md#gate-job-mitigation).

### Usage

```bash
fullsend ci check-e2e-authorization [PR_NUMBER] [OWNER/REPO] [flags]
```

Positional arguments match the bash script. You can also pass `--pr`
and `--repo`, or set `PR_NUMBER` and `GITHUB_REPOSITORY`.

### Environment

| Variable | Used when | Description |
|----------|-----------|-------------|
| `EVENT_NAME` | always | `issue_comment` selects `/ok-to-test` comment mode; empty or `pull_request` / `labeled` / `pull_request_target` use author/label policy; other values are rejected with `reason=error` |
| `COMMENT_ID` | `issue_comment` | Comment id to fetch from the API (required in comment mode) |
| `COMMENTER_LOGIN` | `issue_comment` | Who posted the comment (optional; API author is used when empty) |
| `EVENT_ACTION` | `pull_request` / `labeled` | GitHub event action |
| `PR_UPDATED_AT` | non-`labeled` with stale labels | Frozen `pull_request.updated_at` |
| `PR_AUTHOR_LOGIN` | `pull_request` | Pull request author login |
| `LABEL_ACTOR_LOGIN` | `labeled` | Who applied the label on this run |
| `EVENT_LABEL` | `labeled` | `github.event.label.name` (must be `ok-to-test` to authorize) |
| `CHECK_E2E_AUTH_DRY_RUN` | label removal | Set to `true` to report removal without calling the API |

### Flags

| Flag | Description |
|------|-------------|
| `--pr` | Pull request number (default: positional argument or `PR_NUMBER`) |
| `--repo` | Repository in `owner/repo` form (default: positional argument or `GITHUB_REPOSITORY`) |
| `--event-action` | GitHub event action (default: `EVENT_ACTION`) |
| `--pr-updated-at` | Frozen `pull_request.updated_at` (default: `PR_UPDATED_AT`) |
| `--pr-author-login` | Pull request author login (default: `PR_AUTHOR_LOGIN`) |
| `--label-actor-login` | Who applied the label on labeled events (default: `LABEL_ACTOR_LOGIN`) |
| `--event-label` | `github.event.label.name` on labeled events (default: `EVENT_LABEL`) |
| `--dry-run` | Report label removal without calling the API (env: set `CHECK_E2E_AUTH_DRY_RUN=true` to enable when the flag is omitted) |
| `--forge-token` | GitHub token (default: `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`) |

### Outputs

After a completed check the command exits 0. Callers inspect the
outputs. A missing PR number or repository is a usage error and exits
non-zero.

When `GITHUB_OUTPUT` is set, the command appends:

```
authorized=true|false
reason=<reason>
label_removed=true|false
head_sha=<sha>
```

`head_sha` is set in `issue_comment` mode when the write+ commenter check
passes (even if the named sha is missing or stale). It is empty in other
modes and when the commenter is untrusted.

It also prints one stdout line:

```
authorized=<bool> reason=<reason> label_removed=<bool> head_sha=<sha>
```

If the GitHub API fails or the gate cannot be constructed (for example a
missing token), `reason` is `error`, the cause is written to stderr, and
the command still exits 0.

`reason` values match the bash script: `trusted_bot`, `trusted_author`,
`ok_to_test`, `fresh_ok_to_test`, `stale_ok_to_test`, `other_label`,
`untrusted_labeler`, `untrusted_commenter`, `missing_sha`, `unauthorized`,
`error`.

`other_label` is returned on a `labeled` event when `EVENT_LABEL` names a
label other than `ok-to-test`. Callers should skip gate comments for this
reason (same as `fresh_ok_to_test`).
