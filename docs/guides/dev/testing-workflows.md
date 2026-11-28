# Testing workflow changes

This guide explains how to test changes to fullsend's GitHub Actions workflows, composite actions, and the CLI itself.

## References

There are independent version reference inputs that control different parts of the system:

| Input | Controls | Where set |
|-------|----------|-----------|
| `@<ref>` on `uses:` | Which reusable workflow YAML runs | The `uses:` line in the caller workflow |
| `fullsend_version` | Which fullsend CLI binary is installed | Passed as a `with:` input |

When no release exists for `fullsend_version`, `action.yml` falls back to cloning
and building from source at that ref (see the `install-method=source` path).

The standalone prioritize workflow normally leaves inference credential preparation
to the CLI. With an explicit `fullsend_version` override and both GCP credentials
supplied, it also runs GCP preparation for compatibility with older Vertex CLIs.
OpenAI-only callers without GCP credentials skip this step.

If `uses:` and `fullsend_version` diverge, the workflows/agents and
CLI diverge, potentially causing mismatch in behavior and failures.

## Vendored installs (recommended for PR testing)

Install or re-install a test repository with `--vendor` to copy reusable
workflows, actions, agent definitions, and the CLI binary from your checkout
into its `.fullsend/` directory:

```bash
go run ./cmd/fullsend github setup "$OWNER/$REPO" \
  --vendor \
  --fullsend-source "$PWD" \
  --mint-url "$MINT_URL"
```

After changing reusable workflows or agent content, re-run the same
`go run ./cmd/fullsend github setup "$OWNER/$REPO"` command above, including
`--vendor` and `--fullsend-source "$PWD"`, to refresh the target repository's
vendored files and workflow callers.

Runtime skips the upstream sparse checkout when `.defaults/action.yml` is
present (vendored install) and stages content from `.defaults/` instead.

See [ADR 0047](../../ADRs/0047-vendored-installs-with-vendor-flag.md) for the
full distribution model.

## Layered installs: pin upstream ref

In layered mode (default), thin callers reference upstream reusable workflows at
`fullsend-ai/fullsend@main`. To test a specific upstream ref without vendoring,
change the `uses:` ref and matching `with:` inputs in the thin caller workflows.

**Note**: for forks, change the `fullsend-ai/fullsend` portion to point to your fork.

In your repository modify the dispatch job at `.github/workflows/fullsend.yaml`:

```yaml
# .github/workflows/fullsend.yaml
jobs:
  dispatch:
    # [...]
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@<YOUR_BRANCH>
    with:
      # [...]
      fullsend_version: <YOUR_BRANCH>
      # [...]
```

Push the change and trigger a fullsend action on the test repository:
`/fs-triage`, `/fs-code`, or another agent command. Revert the references to
the desired release or branch before deleting or rewriting the test ref.

## Jira poll batch check

On GitHub, the managed Jira poll workflow passes a cycle's dispatch records
from the poll job to the dispatch job as one job output, and runs each record
as one job of a matrix. GitHub limits a job's outputs to 1 MB and a matrix to
256 jobs. A batch over either limit is lost whole, so the jira-poll driver
keeps a cycle to at most 250 records and 768 KB (ADR 0085).

The `Jira poll batch check` workflow
(`.github/workflows/jira-poll-batch-check.yml`) shows on GitHub that a batch
at those limits arrives whole. It runs one real poll cycle against a fake Jira
(`hack/prove-jira-poll-batch`), so the batch is as large as the driver allows,
and sends it along the same path as the managed workflow: a job output of a
reusable workflow, then an input of a second reusable workflow, then one
matrix job per record. Each matrix job compares the record it received with
the one the batch held, and a last job checks that every record got a job. No
agent runs, and the check needs no Jira site, secrets or mint.

Run it once per case when a change touches the driver's limits, the dispatch
record format, or how `reusable-poll-jira.yml`, the managed caller or
`reusable-dispatch.yml` hand the matrix over:

```bash
gh workflow run jira-poll-batch-check.yml -f case=records
```

| Case | Batch |
|------|-------|
| `records` | 250 records, the record limit, with comments as long as still fit in 768 KB |
| `size` | The largest plain-text records, up to 768 KB |
| `wide` | The largest records of three-byte characters, up to 768 KB |

GitHub counts two bytes for each byte of an output's UTF-8 text, so a batch
at the 768 KB limit is about 384 KB of JSON. Its documentation says only that
the size is "approximated based on UTF-16 encoding". The `wide` case holds the
difference: a count of two bytes per UTF-16 code unit puts three times as many
of its records in a batch, and GitHub rejects that batch with
`Job outputs exceed 1,048,576 bytes`.

A run passes when its `Verify the batch arrived whole` job passes. That job
prints the batch's record count and size. When the poll job's output is over
GitHub's limit, GitHub fails the poll job, the dispatch job is skipped and
the verify job fails.

The check covers the hand-off only. It does not run agents, so it says
nothing about runner capacity or about how long 250 agent runs take.

`make script-test` fails when the hand-off in the real workflows no longer
matches the copy in the check (`.github/scripts/jira-poll-record-digest-test.sh`).

## Running CI checks locally

On macOS, or a Linux distribution other than Ubuntu, `ci-local` runs the PR
CI checks in a container that matches the `ubuntu-24.04` runner, so local
results predict CI.

**Prerequisites:** Podman or Docker, set up as described in
[`hack/ci-local/README.md`](../../../hack/ci-local/README.md#setup).

1. Run everything PR CI runs:

   ```bash
   make ci-local
   ```

2. Run selected targets while iterating:

   ```bash
   make ci-local T="go-test lint-all"
   ```

3. List all targets and options:

   ```bash
   hack/ci-local/ci-local help
   ```

   Common targets are `go-test`, `lint-all`, `script-test` and `docs`. Add
   `-C <path>` to test another checkout or worktree, and `--amd64` to match
   GitHub's x86_64 runners exactly. On Apple silicon, `--amd64` is emulated
   and slow, and needs Docker Desktop's Rosetta emulation (see
   [Setup](../../../hack/ci-local/README.md#setup)):

   ```bash
   CI_LOCAL_ENGINE=docker hack/ci-local/ci-local -C ../my-worktree --amd64 go-test
   ```

The first run builds the image, which takes a few minutes. Set
`CI_LOCAL_ENGINE=docker` to use Docker when Podman is also installed.
`CI_LOCAL_DNS` overrides the resolvers the container uses; see
[DNS](../../../hack/ci-local/README.md#dns). The README also covers
isolation, how tool versions follow CI, and `ci-local clean`.
