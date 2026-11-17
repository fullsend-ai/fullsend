# SPIKE: GitLab projects without `no_one_allowed`

**Issue:** [#9597](https://github.com/fullsend-ai/fullsend/issues/9597) ·
**Related:** [#9601](https://github.com/fullsend-ai/fullsend/issues/9601) (install token scope),
[#9466](https://github.com/fullsend-ai/fullsend/issues/9466) (runtime check of the setting),
[#8871](https://github.com/fullsend-ai/fullsend/issues/8871) (maintainer decisions that use the setting as a gate) ·
**Date:** 2026-11-17

## Status and scope

This document is a spike plan. When the tests are complete, this document
also records the results.

This document does not change runtime behavior, an ADR, or a default.

This document does not identify an adopter. It calls the adopter that
started this spike "adopter A".

[ADR 0131](../ADRs/0131-gitlab-dispatch-pipeline-inputs-no-owner-role.md)
makes `ci_pipeline_variables_minimum_override_role=no_one_allowed` a
required control. Adopter A sets this role to `developer`. Adopter A uses
pipeline variables for CI with other repositories, so it cannot change the
setting.

Today, install does not deliver runnable jobs to a project without
`no_one_allowed`. Thus, no current fullsend installation runs in the
configuration that this spike tests.

The spike must answer two questions:

1. When the setting is `developer` or `maintainer`, what can a pipeline
   variable caller do to the fullsend jobs?
2. Which controls stop that caller?

## Terms

| Term | Meaning |
|---|---|
| D | A test account with the Developer role |
| M | A test account with the Maintainer role |
| X | A test account that is not a project member. X has a trigger token only if the test gives one. |
| Identity pin | [`pin-ci-job-identity.sh`](../../internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/pin-ci-job-identity.sh). All fullsend jobs use this script before they use a credential. |
| Forge credential | The Poller PAT (`FULLSEND_GITLAB_POLLER_TOKEN`), a role credential (`FULLSEND_JOB_TOKEN`), or `CI_JOB_TOKEN` |
| Observed | The result comes from a recorded test run |
| Inferred | The result comes from an observation and the code or the GitLab documentation |
| Unknown | The spike did not find the result. The record gives the reason. |

## Target: same exposure as GitHub

On GitHub, the runtime trust boundary of fullsend is write access to the
repository. At runtime, fullsend does not check branch protection,
rulesets, or CODEOWNERS.

ADR 0033 calls CODEOWNERS entries on the workflow and `.fullsend/` "the
primary defense" against workflow misconfiguration and insider changes to
agent behavior. These entries control the merge path. They do not control
the runtime path below:

- [OpenAI workload identity](../guides/infrastructure/openai-workload-identity.md)
  says that a user with write access can get a model token. The user pushes
  a branch with a workflow to get the token. No merge is necessary.
- ADR 0095 removes `ref` conditions intentionally. The Vertex path also has
  no `ref` condition.
- The spend limit of the service account sets the maximum cost of this
  exposure.
- The mint examines `repository` and the workflow file in
  `job_workflow_ref`. The mint does not examine `ref`
  (`internal/mintcore/claims.go`).

GitHub does not need a setting like `no_one_allowed`. A workflow caller
cannot change `GITHUB_API_URL` or the job token. On GitLab, a pipeline
variable can change the related values. This difference is the reason for
ADR 0131.

| Configuration | Who can get credentials at runtime | Which credentials |
|---|---|---|
| GitHub | A user with write access | Model tokens. The spend limit sets the maximum cost. |
| GitLab with `no_one_allowed` | Only code on the protected default branch | All credentials that the jobs hold |
| GitLab with `developer`, no added controls | A user who can run a pipeline on the default branch (a Developer on adopter A) | The job token, the Poller PAT, and a role credential. The caller can also give the agent MR data that the caller writes, which is a prompt injection input. |

**Proposed target for the ADR:** On GitLab, the exposure at the Developer
role must not be larger than the exposure at write access on GitHub.

- A Developer can get inference tokens. GitHub permits the same exposure.
- A forge credential must not go to a host or project that a variable
  selects.

This target does not use the approval rules or branch settings of an
adopter. The test results show that controls in the job cannot meet this
target (see the `BASH_ENV` test). The decision is
[ADR 0175](../ADRs/0175-run-gitlab-jobs-in-a-companion-project.md): the
fullsend jobs run in a companion project.

## Configuration of adopter A

The adopter A team gave these answers on 2026-11-17:

- Developers can merge to the default branch. No user can push to it.
- Callers set pipeline variables through trigger tokens, manual runs, and
  schedules. Some callers also use `spec:inputs`.

Thus, a Developer on adopter A can run a pipeline on the default branch
with changed variables. This action needs no approval.

## Setup

1. Make a personal GitLab.com group. The test group used the Free tier.
   Code owner approval needs Premium or Ultimate, so test 1.7 needs a group
   on one of these tiers.
2. Make one private test project in the group.
3. Install fullsend at the current `main`. Enable only the review agent.
4. Make a second installation at an older release that adopters can pin.
5. Make the test accounts D, M, and X.
6. Protect `main` with the adopter A settings: push permitted for no user,
   merge permitted for Developers and Maintainers.
7. Set "Minimum role to use pipeline variables" to `developer`.
8. Add a test job on a temporary branch of the scaffold. The job must write
   only the length and a short SHA-256 prefix of each sensitive variable.
   The job must not write the value. The job must also write the result of
   `GET /api/v4/job`.
9. Use false values for the Poller PAT and the role credentials.

Do the key tests again with these changes:

- Merge to `main` permitted for Maintainers only. Other adopters can have
  this setting.
- "Minimum role to use pipeline variables" set to `maintainer`.

## Tests

### Test group 1: Which users can start a pipeline that the jobs accept

The identity pin stops the job when the job ref is not the protected
default branch. GitLab lets a user run a pipeline on a protected branch
only if the user can push or merge to that branch.

| No. | User and procedure | Ref | Expected result |
|---|---|---|---|
| 1.1 | D: `POST /projects/:id/pipeline` with variables. Merge to `main` is for Maintainers only. | `main` | HTTP 403 |
| 1.2 | D: "Run pipeline" in the UI with variables. Merge to `main` is for Maintainers only. | `main` | GitLab stops the pipeline, as in 1.1 |
| 1.3 | D: the procedure of 1.1 on a branch of D | `feature` | The pipeline runs. The identity pin stops the fullsend jobs. The jobs get no protected variables. |
| 1.4 | D: open an MR from `feature` | MR pipeline | Same as 1.3. Also examine `merge_request_event` pipelines. |
| 1.5 | M: `POST /projects/:id/pipeline` with variables | `main` | The pipeline runs. The #8871 decisions already trust Maintainers. |
| 1.6 | D: the procedure of 1.1 with merge permitted for Developers | `main` | The pipeline runs. This is the adopter A configuration. |
| 1.7 | D: open an MR that changes `.gitlab-ci.yml` or `.gitlab/ci/scripts/*`, then merge it. Needs Premium or Ultimate. | `main` | The result depends on the approval rules and CODEOWNERS. If D can merge without approval, D already controls the fullsend jobs. |

### Test group 2: What a changed variable does (pipelines from 1.5 and 1.6)

Do these steps for each variable in this list: `CI_JOB_TOKEN`,
`CI_API_V4_URL`, `CI_SERVER_URL`, `CI_SERVER_HOST`, `CI_PROJECT_PATH`,
`CI_PROJECT_ID`, `CI_PIPELINE_SOURCE`, `CI_COMMIT_REF_PROTECTED`,
`FULLSEND_GITLAB_URL`, and a protected variable that install writes.

1. Send the pipeline request with a changed value.
2. Record the HTTP status. HTTP 201 means that GitLab accepted the value.
   HTTP 400 means that GitLab refused the value.
3. Record the value that the job got.
4. Record the result of the identity pin.

Expected results:

- An incorrect `CI_JOB_TOKEN` causes `GET /job` to fail.
- The identity pin ignores a changed `CI_PIPELINE_SOURCE` and a changed
  `CI_COMMIT_REF_PROTECTED`. The identity pin reads `.source` and the ref
  from the job record.

**Most important test:** Set `CI_API_V4_URL` to a host that you control.
Put a listener on that host. The listener must record only the request
path, the token header name, the token length, and a short SHA-256 prefix.
The listener must not keep a token value. Do the test two times:

- The listener returns HTTP 404 for all requests except `GET /job`.
- The listener also returns correct project, branch, and pipeline records.

Also do this test: get a valid job token from a job in a different project
that D can access. Use that token as `CI_JOB_TOKEN`. The script comment
says that the identity pin then uses the job record of the other job.
Record the result.

#### Results of test group 2 (2026-11-17)

Environment: GitLab.com `19.6.0-pre` (revision `c21bed860a6`), Free tier,
SaaS shared runners, a private personal test project. The test user had
the Owner role. The test did not install fullsend. A probe job on `main`
wrote each value to the log. For token values, the job wrote only the
length and a short SHA-256 prefix. The project had a protected variable
`FULLSEND_GITLAB_POLLER_TOKEN` with a false value.

| Pipeline | Role setting | Changed variables | GitLab response | What the job got |
|---|---|---|---|---|
| Control | `no_one_allowed` | `CI_API_V4_URL` | HTTP 400, "Insufficient permissions to set pipeline variables" | No pipeline |
| A2 | `maintainer` | `CI_API_V4_URL`, `CI_SERVER_URL`, `CI_SERVER_HOST`, `CI_PROJECT_ID`, `CI_PIPELINE_SOURCE`, `CI_COMMIT_REF_PROTECTED`, `FULLSEND_GITLAB_URL` | HTTP 201 | All seven changed values. The real `CI_JOB_TOKEN` (800 characters) and the protected Poller PAT were also present. |
| A | `maintainer` | The A2 list and `CI_PROJECT_PATH` | HTTP 201 | The job failed before the script. The runner used the changed `CI_PROJECT_PATH` in the clone URL. |
| A3 | `maintainer` | `CI_PROJECT_PATH`, `GIT_STRATEGY=none` | HTTP 201 | The changed `CI_PROJECT_PATH` |
| B | `maintainer` | `CI_JOB_TOKEN` | HTTP 201 | The changed `CI_JOB_TOKEN`. `GET /job` with this token returned no job record. |
| C | `maintainer` | `FULLSEND_GITLAB_POLLER_TOKEN` (a protected variable that install writes) | HTTP 201 | The caller value, not the protected project value |

**Observed:**

- When the role setting permits the caller, GitLab accepts a changed value
  for each predefined variable in the test. The job gets the changed value.
- A changed `CI_API_V4_URL` does not remove the real `CI_JOB_TOKEN` or the
  protected variables from the job.
- A pipeline variable replaces a protected project variable with the same
  name. Thus, a value that install writes as a CI variable is not a trusted
  value.
- `no_one_allowed` stops the change at the API with HTTP 400.

**Inferred from the identity pin code:**

- The first request of the identity pin is `GET ${CI_API_V4_URL}/job` with
  `JOB-TOKEN: ${CI_JOB_TOKEN}` (line 240). With pipeline A2, this request
  sends the real job token to the host that the caller selected.
- The `https://*/api/v4` format check and the character check do not stop
  this, because the caller can use a correct HTTPS host.
- A changed `CI_JOB_TOKEN` makes the identity pin fail closed, as the
  script comment expects.

#### End to end test of the identity pin (2026-11-17)

The test used `pin-ci-job-identity.sh` and `trust-ci-server-ca.sh` without
changes, from `fullsend-ai/fullsend@d7dd6dd06`. A listener ran in the job
on `https://localhost:8443`. The listener replaced the host that a caller
selects. The listener wrote only the request path, the token header name,
the token length, and a short SHA-256 prefix. The Poller PAT and the role
credential had false values.

The listener used a test certificate. The harness gave this certificate to
the identity pin through `CI_SERVER_TLS_CA_FILE`. In a real attack, the
caller uses a public host with a valid certificate, so the caller needs no
CA.

| Pipeline | Listener behavior | Identity pin result | Requests at the listener |
|---|---|---|---|
| Baseline | None (`CI_API_V4_URL` not changed) | Failed closed: "cannot read pinned project" | None |
| Attack | HTTP 404 for all requests except `GET /job` | Failed closed: "cannot read pinned project" | `GET /job` with the job token. Then `GET /projects/:id` 4 times with the job token, and 4 times with `PRIVATE-TOKEN` (the Poller PAT). |
| Cooperating attack | Correct project, branch, and pipeline records | Success. `FULLSEND_PINNED_GITLAB_URL` was the listener. `FULLSEND_PINNED_PROJECT_PATH` was the path that the listener returned. | `GET /job`, `/projects/:id`, `/repository/branches/main`, and `/pipelines/1` with the job token. Then `GET /pipelines/1` with `PRIVATE-TOKEN` (the role credential). |

**Observed:**

- The token hashes at the listener are the same as the token hashes in
  the job (job token, Poller PAT, and role credential).
- In the attack pipeline, the identity pin failed closed, but only after
  it sent the job token and the Poller PAT. The `curl --retry 3` option
  sends each token four times.
- In the cooperating attack pipeline, the identity pin accepted the
  listener as the pinned GitLab URL. The next helper
  (`fullsend_pinned_pipeline_creator_id`) sent the role credential to the
  listener.
- In the baseline, `GET /projects/:id` with the job token also failed on
  GitLab.com. Thus, on GitLab.com, the identity pin always uses the Poller
  PAT fallback. (The baseline fallback failed because the PAT in the test
  is false.)

**Inferred:** After a cooperating pin, the agent job uses the pinned URL
for all later forge requests (`run-agent-job.sh`, `run-poll-job.sh`). The
host of the caller then supplies the MR data that the agent reads. This
data is a prompt injection input for an agent that holds model
credentials.

**Result:** With a relaxed role, one pipeline caller gets the job token,
the Poller PAT, and a role credential of a fullsend job. The caller can
also supply the MR data that the agent reads. The caller needs no change
to the repository.

#### Test of code execution through `BASH_ENV` (2026-11-17)

The caller sent a pipeline variable `BASH_ENV` of type `file`. The file
contained one shell command. This command wrote the length and a short
SHA-256 prefix of the Poller PAT and the length of `CI_JOB_TOKEN`.

| Variable options | Result |
|---|---|
| Default (`raw: false`) | The command ran in each `bash` process of the job. GitLab expanded the `$` references at pipeline creation, so the values were empty. |
| `raw: true` | The command ran before the job script. It wrote the length and hash prefix of the Poller PAT and the length of the job token. These values are the same as the protected variable and the real job token. |

**Observed:** With a relaxed role, a pipeline caller can run a command of
the caller in a fullsend job. The command has access to all variables of
the job, including the protected variables and `CI_JOB_TOKEN`.

**Inferred:**

- Bash reads `BASH_ENV` before the first line of a script. A job script
  cannot stop it. Other variables can also run code of the caller, for
  example `ENV`, `LD_PRELOAD`, `NODE_OPTIONS`, `PYTHONSTARTUP`,
  `GIT_CONFIG_*`, and `GIT_SSH_COMMAND`.
- A list of blocked variables in the job is not a reliable control.
- Thus, **outcome 2 is not sufficient**. Host pinning stops only one
  route. It does not stop code execution through the environment.
- The remaining outcomes are outcome 1 (only Maintainers and Owners can
  run a pipeline on the default branch) and outcome 3 (the fullsend jobs
  run in a separate project with `no_one_allowed`).

### Test group 3: Trigger tokens and schedules

| No. | Procedure | Record |
|---|---|---|
| 3.1 | X uses the webhook trigger token. X sends variables to `POST /trigger/pipeline` on `main`. The setting is `developer`. | Does the pipeline start? The pipeline uses the role of the token owner. |
| 3.2 | Do 3.1 again with a false `TRIGGER_PAYLOAD`. | The dispatcher probably accepts the payload format. For this reason, the plan keeps the webhook path available only with `no_one_allowed`. |
| 3.3 | Examine a schedule that the Poller identity owns. | Schedule variables must come only from the schedule definition. D must not be able to change a schedule that D does not own. |

### Test group 4: Inference credentials

| No. | Procedure | Record |
|---|---|---|
| 4.1 | Examine the `id_tokens` JWT for each changed variable in test group 2. | `project_path`, `ref`, and `ref_protected` must come from GitLab, not from variables. |
| 4.2 | Do a Vertex WIF exchange from a job on `feature`. Add the condition `assertion.ref_protected == 'true'`. | The current documented condition examines only `project_path` and `project_id`. Does the added condition stop `feature` and permit `main`? |

### Test group 5: A separate fullsend project (option B)

This test is small. It prevents a second spike for option B.

1. Make the project `<group>/fullsend-for-test`. Set `no_one_allowed` on
   this project only.
2. Keep the `developer` setting on the target project.
3. From the fullsend project, run the review agent on an MR in the target
   project. Use only the current service account credentials.
4. Record which functions fail. Examine the pipeline status and notes on
   the target MR, the runner tags, and the `CI_JOB_TOKEN` scope.
5. From the target project, start a fullsend project pipeline with a
   pipeline trigger token.
6. From the target project, start a fullsend project pipeline with a
   service account membership and `trigger: project:`.
7. Record which procedure does not need a secret in the target project.

## Exit criteria

The spike is complete when each test has an Observed result, or an Unknown
result with a reason. Then write a summary of one page. The summary must
recommend one of these outcomes:

1. **Policy rule only.** Accept `developer` or `maintainer` when install
   can read these three conditions:
   - The default branch is protected.
   - Only Maintainers can merge or push to the default branch.
   - The fullsend credentials are protected variables.

   Keep the webhook path off when the setting is not `no_one_allowed`.
   This outcome is not sufficient for adopter A, because its Developers
   can merge.
2. **Controls in the job.** Do not use the Maintainer condition of
   outcome 1. Get the trusted host and project only from files that
   install commits on the protected default branch, or from claims that
   GitLab signs, such as `id_tokens`. Do not get them from CI variables,
   because a pipeline variable replaces them (pipeline C). Also add a
   `ref_protected` WIF condition. **The `BASH_ENV` test rejects this
   outcome**, because the code of the caller runs before these controls.
3. **Separate fullsend project (option B).** Select this outcome if test
   group 1 shows that Developers can get to the protected default branch
   in configurations that fullsend cannot detect. The results of test
   group 2 and the `BASH_ENV` test also point to this outcome. Test
   group 5 shows the cost.

The recommendation goes into a new ADR. The ADR must also examine the
#8871 decisions again, because those decisions use `no_one_allowed` as the
gate.
