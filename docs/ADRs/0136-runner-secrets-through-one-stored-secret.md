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
but on GitHub a user secret such as a Jira token never reaches `fullsend run`:
the reusable workflow receives only the secrets it declares, and the shim is a
generated file that setup and `repos install` overwrite (#7689).

Credentials stay out of the sandbox
([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)), workflow
`env:` carries infrastructure plumbing only
([ADR 0081](0081-reserve-workflow-env-for-infra-plumbing.md)),
`GH_WORKFLOW_TOKEN` must stay unforgeable
([ADR 0114](0114-github-packages-via-host-bound-workflow-token-provider.md)),
and an overlay may set any field once guarded fields are checked
([ADR 0112](0112-overlays-may-set-any-harness-field.md)).

## Options

1. **Caller-side list in the shim** (`format(...)` with `toJSON(secrets.X)`).
   Users would edit a generated file.
2. **`secrets: inherit` with `toJSON(secrets)`.** Every repository secret
   would reach the runner.
3. **One named secret per integration.** Every new integration would need a
   fullsend release.

## Decision

On GitHub, user secrets reach host-side scripts through one optional stored
secret, `FULLSEND_RUNNER_SECRETS`, a JSON object of name → string value. The
reusable workflow passes it to each stage's composite action; a staging step
writes it to a mode 0600 file under `RUNNER_TEMP` and hands `fullsend run`
only the path. `fullsend run` deletes the file and unsets the path before any
child starts, then resolves `${NAME}` in the resolved `env.runner` (after
overlays) from the object. Only the short staging shell ever holds the object
in its environment; it is never in the environment of `fullsend run` or of
any process it starts, and keys the harness does not reference reach no
script. Referenced values reach their scripts as environment variables, and
scripts still run as the job user. Locally, `FULLSEND_RUNNER_SECRETS` may
carry the object inline.

These rules apply to names in the GitHub object:

- **Refused names:** `oidcDenyKeys`, `providerOnlyKeys`, sandbox-reserved
  names, the `FULLSEND_`, `GITHUB_`, `ACTIONS_`, `RUNNER_`, `CI_` and `LD_`
  families, `GH_TOKEN`, the minted role tokens, `GITLAB_TOKEN` and `PATH`.
  Null values, duplicate keys and values shorter than the redactor's minimum
  are refused too.
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
because the log masks the stored string, not the fields inside it. Three
controls compensate: `fullsend run` masks each value and each of its lines
with an escaped `::add-mask::`; it registers each value, each line and the
JSON-escaped form with the redactor that cleans failure detail and validation
feedback; and file staging keeps the object out of the environment of
`fullsend run` and its children. Their limits: masking and redaction match
exact strings, so a transformed value (base64, URL-encoded, a substring) is
not hidden; a line shorter than the redactor's minimum is masked in the log
but not redacted; and a script that receives a value can still leak it.

## Consequences

- Adding an integration is one `gh secret set` and one `env.runner` line, with
  no shim edit and no fullsend release.
- `GH_WORKFLOW_TOKEN` and the minted role tokens stay unforgeable, because
  their names are refused as keys.
- The shim always passes the secret, and GitHub rejects a call that passes a
  secret the called workflow does not declare. A shim rendered with
  `--fullsend-ref` pointing at a release before this one therefore fails; an
  older `--fullsend-ref` needs a matching older CLI.
- Known gap: until ADR 0112's guarded-field check exists, an event-guarded
  overlay can still swap `pre_script`, `post_script` or `validation_loop`, and
  so choose which script receives a referenced secret.
- Updating one value means rewriting the whole object.
