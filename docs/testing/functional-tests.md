# Functional Tests

Functional tests exercise the full agent pipeline — pre-script, agent
execution, post-script — against ephemeral GitHub fixtures. They verify that
agents produce the right side effects (labels, comments, PR state) when given
controlled inputs.

The suite lives in
[fullsend-ai/agents](https://github.com/fullsend-ai/agents/tree/main/eval),
next to the agent definitions it tests. This repo has no functional-test
suite of its own. For the decision, see
[ADR 0139](../ADRs/0139-agent-functional-test-suite-lives-in-agents-repo.md),
which supersedes the `eval/` layout in
[ADR 0052](../ADRs/0052-functional-tests-for-agent-pipelines.md). For the
framework choice, see
[ADR 0051](../ADRs/0051-agent-eval-harness-for-test-infrastructure.md). For
the broader testing problem, see
[testing-agents.md](../problems/testing-agents.md).

## When they run against fullsend

| Run | Where | Tier | Cases |
|-----|-------|------|-------|
| Release gate | `release.yml` here, calling the agents `functional-tests.yml` | release | Each agent's `release: true` case (for triage, case 001) |
| Nightly | agents `functional-tests-nightly.yml`, against fullsend `main` | full | Every case for every agent |
| Agents changes | agents PRs and merge queue | release on PRs, full in the merge queue | Per the agents workflow |

A fullsend PR does not run functional tests. Before ADR 0139, this repo ran
its own triage suite on PRs that touched `internal/scaffold/` or
`internal/cli/run.go`. The agents nightly replaces that per-PR run: a
regression from a fullsend change shows up in the nightly within a day of
merging, instead of on the PR.

`release.yml` calls the agents workflow without a `tier` input. A cross-repo
call defaults to the release tier, so a release runs one case per agent, not
the full suite.

## Writing and running cases

Cases, judges, thresholds, and the runner scripts are documented in the
agents repo's [eval README](https://github.com/fullsend-ai/agents/blob/main/eval/README.md).
Adding or changing a case is an agents-repo PR.

## Secrets kept here

The release gate's call into agents needs the GCP Workload Identity secrets
(`E2E_GCP_WIF_PROVIDER`, `E2E_GCP_SERVICE_ACCOUNT`, `E2E_GCP_PROJECT_ID`) and
`EVAL_GH_TOKEN`, a fine-grained PAT scoped to the eval org. They stay in this
repo's secrets for that call.
