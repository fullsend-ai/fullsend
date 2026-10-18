---
title: "136. Pass user secrets to host-side scripts through one stored secret"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - harness
  - secrets
  - github-actions
---

# 136. Pass user secrets to host-side scripts through one stored secret

Date: 2026-10-07

## Status

Accepted

## Context

A custom harness's `pre_script`, `post_script` and `validation_loop` read
`${NAME}` through `env.runner` ([ADR 0055](0055-unified-env-var-delivery.md)),
but on GitHub the reusable workflow receives only the secrets it declares. For
a user's own tools that is one fixed pair, `JIRA_TOKEN` and `JIRA_USER_EMAIL`,
which only the `harness-run` job forwards, from a caller the user writes (the
Jira poller). Any other secret, or Jira on another stage, cannot reach
`fullsend run`, and the shim is a generated file that setup and
`repos install` overwrite (#7689, #6359). Once the runner's filtered entity
snapshot ([ADR 0141](0141-deterministic-filtered-entity-context-staging.md))
supplies forge context, the remaining need is tools the user picks, such as a
third-party review CLI.

Constraints: credentials stay out of the sandbox ([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)),
workflow `env:` is infrastructure only ([ADR 0081](0081-reserve-workflow-env-for-infra-plumbing.md)),
`GH_WORKFLOW_TOKEN` stays unforgeable ([ADR 0114](0114-github-packages-via-host-bound-workflow-token-provider.md)),
and overlays may set guarded fields ([ADR 0112](0112-overlays-may-set-any-harness-field.md)).

## Options

1. **Caller-side list in the shim** (`format(...)`): edits a generated file.
2. **`secrets: inherit`:** every repository secret would reach the runner.
3. **One named secret per integration**, as
   [ADR 0138](0138-inference-gateway-api-key-credential-mode.md) does for the
   inference gateway on both forges. Right for integrations fullsend owns, and
   kept for them; it cannot name a tool the user picks without a release.

## Decision

As an interim adoption of #6359's passthrough-map option, on GitHub a
custom harness's own secrets reach host-side scripts through one optional
stored secret, `FULLSEND_GITHUB_RUNNER_SECRETS`, a JSON object of name →
string value. The name carries `GITHUB` because the channel exists only to
get past GitHub's declared-secrets rule for reusable workflows. It covers
host-side scripts only, never sandbox credentials, and does not replace named
secrets for integrations fullsend owns. Each stage's composite action stages
the object in a mode 0600 file under `RUNNER_TEMP` and hands `fullsend run`
only the path; `fullsend run` deletes the file and unsets the path before any
child starts, then resolves `${NAME}` in the resolved `env.runner` (after
overlays). The object is never in the environment of `fullsend run` or its
children; unreferenced keys reach no script, and scripts still run as the job
user. Locally, `FULLSEND_GITHUB_RUNNER_SECRETS` may carry the object inline.

These rules apply to names in the GitHub object:

- **Refused names:** runner-owned names and families (credential, provider,
  sandbox-reserved, `FULLSEND_`, `GITHUB_`, `OTEL_` and more; the harness
  reference lists them all) and every name the workflow sets for the
  `fullsend run` step, such as `JIRA_TOKEN`, so each name has one source.
  Null values, duplicate keys and too-short values are refused too.
- **Host side only:** a reference from `env.sandbox`, `runner_env`, a provider
  credential, a `host_files` source or expanded content, or a
  `validation_loop` field fails validation.
- **Overlays (interim):** an overlay that references an object name may be
  guarded only by `runtime.forge` and `config`, until ADR 0112's guarded-field
  check is implemented.

GitLab is unchanged: a JSON bundle does not fit GitLab's per-variable masking,
each secret stays its own masked CI/CD variable, and none of these rules apply
there. Keeping fullsend's own credentials out of GitLab host-side scripts is
#8146.

GitHub's hardening guidance advises against structured secrets such as JSON,
because the log masks the stored string, not its fields. `fullsend run`
compensates by masking each value and line with an escaped `::add-mask::`,
registering them with the redactor, and staging the object in a file. These
match exact strings only, so a transformed value is not hidden, and a script
that receives a value can still leak it.

## Consequences

- Adding an integration is one `gh secret set` and one `env.runner` line, with
  no shim edit and no fullsend release; updating one value rewrites the object.
- `GH_WORKFLOW_TOKEN` and the minted role tokens stay unforgeable, because
  their names are refused as keys.
- The shim always passes the secret, and GitHub rejects a call that passes a
  secret the called workflow does not declare, so a shim rendered with an
  older `fullsend_ref` fails; it needs the CLI from that release. #7872
  tracks detecting the mismatch.
- The expected successor is external secret manager support: the owner
  declares a store (such as Vault through JWT auth, or GCP Secret Manager
  through WIF) and `fullsend run` reads the referenced keys with the job's
  OIDC token, so no secret travels through the workflow. That ADR, or the one
  settling #6359, ends the interim role; the stored secret stays as the
  fallback for workflows that cannot use GitHub OIDC or a WIF provider with
  the user's secret manager.
- An OpenShell provider is not that successor: credential drivers keep copies
  of values the gateway is given, without reading existing backend secrets
  ([OpenShell #1931](https://github.com/NVIDIA/OpenShell/issues/1931)), and a
  provider delivers only into the sandbox, never to host-side scripts.
- Known gap: until ADR 0112's guarded-field check exists, an event-guarded
  overlay can still swap `pre_script`, `post_script` or `validation_loop`, and
  so choose which script receives a referenced secret.
