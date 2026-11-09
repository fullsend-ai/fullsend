---
sidebar_label: fullsend ci
---

# fullsend ci

CI helper commands. These run inside GitHub Actions. They are not
interactive operator tools.

## fullsend ci check-e2e-authorization

Decide whether a pull request may run credentialed e2e tests.

The command authorizes a run when the PR author is a trusted bot, when
the collaborator permission API confirms write access or higher, or when
this run is an `ok-to-test` labeled event from a user with write access.
A labeled event authorizes only when the event label is `ok-to-test`.
Other labels do not authorize, even if `ok-to-test` is already on the
pull request.

This is the Go replacement for
[`scripts/check-e2e-authorization.sh`](../../scripts/check-e2e-authorization.sh).
Production e2e workflows still run that script. A later change will
point
[`.github/actions/check-e2e-authorization`](../../.github/actions/check-e2e-authorization/action.yml)
at this command ([#8371](https://github.com/fullsend-ai/fullsend/issues/8371)).

For the policy itself, see [E2E testing](../guides/dev/e2e-testing.md)
and [CI workflows](../contributing/ci-workflows.md#gate-job-mitigation).

### Usage

```bash
fullsend ci check-e2e-authorization [PR_NUMBER] [OWNER/REPO] [flags]
```

Positional arguments match the bash script. You can also pass `--pr`
and `--repo`, or set `PR_NUMBER` and `GITHUB_REPOSITORY`.

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
| `--dry-run` | Report label removal without calling the API (default: `CHECK_E2E_AUTH_DRY_RUN=true`) |
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
```

It also prints one stdout line:

```
authorized=<bool> reason=<reason> label_removed=<bool>
```

If the GitHub API fails, `reason` is `error`, the cause is written to
stderr, and the command still exits 0.

`reason` values match the bash script (`trusted_bot`, `trusted_author`,
`ok_to_test`, `fresh_ok_to_test`, `stale_ok_to_test`,
`untrusted_labeler`, `unauthorized`, `error`).
