# Operations

Day-2 administration for fullsend per-repo installations: configuration updates, workflow syncing, uninstall, and standalone commands for split-responsibility workflows. For per-org operations (enrollment, org-level status, org uninstall), see [Per-Org Mode](org-mode.md).

## Prerequisites

- **fullsend CLI** installed (see [Getting Started](../getting-started/))

The remaining prerequisites are forge-specific:

**GitHub:**

- **GitHub access** — repository admin for the target repository
- **`gh` CLI** authenticated with the required OAuth scopes (see [OAuth scope reference](../infrastructure/advanced-setup.md#oauth-scope-reference))

**GitLab:** none of the GitHub-specific prerequisites above apply — GitLab
does not use `gh`. See [Configuring GitLab § Prerequisites](configuring-gitlab.md#prerequisites)
for the GitLab access token and permissions needed for the day-2 tasks
documented below.

## Updating configuration values

### GitHub

Update individual secrets or variables without re-running full setup:

```bash
fullsend github set "$OWNER/$REPO" FULLSEND_GCP_PROJECT_ID new-gcp-project
fullsend github set "$OWNER/$REPO" FULLSEND_GCP_REGION global
```

| Key | Storage Type | Description | Example value |
|-----|-------------|-------------|---------------|
| `FULLSEND_GCP_REGION` | Repo variable | GCP region for Agent Platform inference | `global` |
| `FULLSEND_REVIEW_CLIENT_ID` | Repo variable | OAuth client ID of the review agent's GitHub App (best-effort, auto-set by installer) | `Iv23li1nIorNLIQy6NWK` |
| `FULLSEND_GCP_PROJECT_ID` | Repo secret | GCP project ID where Agent Platform is enabled | `my-gcp-project` |
| `FULLSEND_GCP_WIF_PROVIDER` | Repo secret | Full WIF provider resource name for OIDC authentication | `projects/123456789/locations/global/...` |
| `FULLSEND_OPENAI_API_KEY` | Repo secret | Opt-in OpenAI API key when OpenAI WIF is unavailable (exported as `OPENAI_API_KEY`; unused when the WIF trio is set) | `sk-...` |

### GitLab

For initial GitLab setup, see [Configuring GitLab](configuring-gitlab.md). Secrets are checked for presence only, so `repos install` cannot update the *value* of an existing `FULLSEND_GCP_PROJECT_ID` or `FULLSEND_GCP_WIF_PROVIDER` — once those CI/CD secrets exist, a new `--inference-project` is a silent no-op for them. To change either one, edit the CI/CD variable directly in GitLab (Settings → CI/CD → Variables), since converge cannot read secret values back to compare or overwrite them.

`FULLSEND_GCP_REGION` is also a variable (not a secret), but converge treats
`--inference-project` and `--inference-region` as an all-or-nothing set when
`--inference-wif-provider` isn't set — passing
`--inference-region` alone fails with `incomplete inference flags`. To update
the region through converge, pass both flags, for example
`fullsend repos install <group/project> --inference-project <id>
--inference-region <region>`. This updates the plain `FULLSEND_GCP_REGION`
variable; the presence-only-checked project and WIF-provider secrets still
must be edited directly in GitLab (Settings → CI/CD → Variables). Editing the
region there remains an alternative.

| Key | Storage Type | Description | Example value |
|-----|-------------|-------------|---------------|
| `FULLSEND_FORGE_TOKEN` | CI/CD secret | Shared bot project access token; auto-provisioned by `repos install` | (masked) |
| `FULLSEND_GCP_REGION` | CI/CD variable | GCP region for Agent Platform inference | `us-central1` |
| `FULLSEND_GCP_PROJECT_ID` | CI/CD secret | GCP project ID for inference | `my-gcp-project` |
| `FULLSEND_GCP_WIF_PROVIDER` | CI/CD secret | WIF provider resource name for inference | `projects/123456789/locations/global/...` |
| `FULLSEND_DISPATCH_SECRET` | CI/CD secret | HMAC secret for dispatch variables and poll-state documents; auto-provisioned by `repos install` | (generated) |
| `FULLSEND_GITLAB_ROLE_MIGRATION` | CI/CD variable (protected, unmasked) | Role-credential migration gate (`disabled`, `migrating`, `rollback`, `enforced`). Ordinary unflagged `repos install` writes `migrating` while provisioning, then `enforced` once roles are ready. See [gitlab-role-credentials.md](../../contributing/gitlab-role-credentials.md) | `enforced` |
| `FULLSEND_GITLAB_ROLE_REGISTRY` | CI/CD variable (protected, unmasked) | Administrator role registry (JSON references and policy, not secret values); empty means built-in roles only. Written by `repos install --gitlab-role-registry`. | `{"roles":[]}` |
| `FULLSEND_GITLAB_ROLE_ROTATION` | CI/CD variable (protected, unmasked) | Per-role rotation state (lock, token IDs, expiry dates, phase). Never stores token values. Written by `repos install` during rotation. | `{"roles":{}}` |
| `FULLSEND_GITLAB_POLLER_TOKEN` / `FULLSEND_GITLAB_ANALYST_TOKEN` / `FULLSEND_GITLAB_CODER_TOKEN` | CI/CD secret | Built-in role PATs provisioned by `repos install`. Absence is not a health failure while the gate is `disabled` or during partial `migrating`. | (masked) |
| `OPENAI_API_KEY` | CI/CD variable (masked) | Opt-in static OpenAI API key when OpenAI WIF is unavailable; unused when the WIF trio is set | `sk-...` |

Ordinary unflagged `repos install` cuts over GitLab role credentials when they are ready and deletes `FULLSEND_FORGE_TOKEN`. Drain in-flight shared-token jobs before that converge. [`--gitlab-role-cutover --gitlab-role-cutover-drained`](../../cli/repos.md#gitlab-role-cutover) is an explicit fail-closed retry. Missing role credentials are reported as drift when the gate is `enforced`. Emergency rollback uses `--gitlab-role-migration=rollback --gitlab-role-rollback-confirmed`.

## Syncing workflow templates

After upgrading the fullsend CLI, re-run `github setup` to update the workflow file for a single repo:

```bash
fullsend github setup "$OWNER/$REPO" \
  --inference-project "<GCP_PROJECT>" \
  --inference-wif-provider "<WIF_PROVIDER>"
```

For manifest-managed installations (including GitLab repos), use `repos install` to converge all repos (including workflow ref upgrades):

```bash
fullsend repos install -f repos.yaml
```

This is idempotent — it provisions new repos, repairs missing or drifted components (workflow, thin callers, variables, secrets, pipeline schedules), repairs scaffold content drift, refreshes a declared configuration preset (`.fullsend/config.base.yaml`), and upgrades workflow refs. Variables with manifest-specified values (e.g. mint URL, GCP region, review app client ID) are checked for value drift; secrets and runtime-mutated variables are checked for presence only. For GitLab repos, converge also migrates any leftover legacy poll-state CI/CD variables (from installs predating #7380) into the HMAC-signed poll-state branches and deletes them, so they stop being seeded or reported as orphans. Converge also migrates the root `.gitlab-ci.yml`: an obsolete `merge_request_event` workflow rule left over from installs predating the removal of native MR dispatch (#7322) is stripped automatically, without disturbing any other fullsend or user-owned entries in the file. This automatic migration applies only to repos where fullsend owns the `workflow:` block (fresh installs, identified by the fullsend-generated `workflow.name`); repos enrolled by merging fullsend rules into a pre-existing `workflow:` block have no such ownership marker and are left untouched — remove the leftover `merge_request_event` rule from those manually, as described in the uninstall steps below. Converge separately strips a leftover empty `dispatch` stage from `stages:`, left over from installs predating the removal of the empty dispatch stage (#7337). This migration is gated differently: it applies whenever the file has the fullsend pipeline include and at least one current fullsend stage (`poll` or `agent`) already in `stages:`, and it additionally scans every job definition in the file — including ones reached only via `extends:` or the YAML merge key (`<<:`) — for a still-live reference to `dispatch`, leaving the stage in place if any job depends on it.

## Uninstalling

### Per-repo teardown

To remove fullsend from a single repository:

**GitHub repos:**

1. Delete `.github/workflows/fullsend.yaml`, `.github/workflows/prioritize.yml`, and repo-level secrets/variables
2. Run `fullsend inference deprovision "$OWNER/$REPO"` to remove WIF access
3. Remove the `FULLSEND_MINT_URL` repository variable (if set) — no separate unenrollment is needed for the hosted community mint

**GitLab repos:**

1. Run `fullsend repos uninstall "$PROJECT_PATH"` (the full `group/subgroup/project` path) to open a PR that removes fullsend entries from `.gitlab-ci.yml` and deletes `.gitlab/ci/fullsend-pipeline.yml`, `.gitlab/ci/fullsend-agent.yml`, `.gitlab/ci/fullsend-dispatch.yml`, `.gitlab/ci/fullsend-poll.yml`, `.gitlab/ci/scripts/trust-ci-server-ca.sh`, and `.fullsend/config.yaml` (pass `--direct` to push those file changes to the default branch). Variables, secrets, and the `fullsend-poll-state-slash` / `fullsend-poll-state-events` branches are deleted immediately via the API. If you prefer manual removal: delete `.gitlab/ci/fullsend-*.yml`, `.gitlab/ci/scripts/trust-ci-server-ca.sh`, and `.fullsend/config.yaml`, then edit `.gitlab-ci.yml` to remove the fullsend pipeline include entry, the fullsend stages (`poll`, `agent`, and `dispatch` on installs from before the empty dispatch stage was removed), the fullsend workflow rules (`schedule`, `api`, and `merge_request_event` on installs from before native MR dispatch was removed), and the `auto_cancel` block if fullsend added it. Only delete `.gitlab-ci.yml` entirely if it contains no non-fullsend configuration. Also delete the two poll-state branches if they are still present.

> **Note:** During install, fullsend sets `workflow.auto_cancel.on_new_commit: none` when an existing, non-empty `.gitlab-ci.yml` already has a `workflow:` block and that key is missing; it does not overwrite an existing value. If that existing block has no `rules:` key, fullsend also adds protected-ref `schedule`/`api` rules, which can stop ordinary push pipelines unless the block already has a matching rule; add a catch-all/push rule (or an explicit `when: always` rule) before installing, or remove the name-only block so fullsend can leave `workflow:` absent. If an existing, non-empty file has no `workflow:` block, fullsend leaves it absent so push-triggered pipelines are not disrupted. For a missing or empty `.gitlab-ci.yml`, fullsend writes a fullsend-owned `workflow:` block with protected-ref `schedule`/`api` rules; later push jobs added to that file likewise need additional `workflow.rules` or an explicit `when: always` rule, or GitLab will skip them. Repos with `on_new_commit: interruptible` (or other non-`none` values) may experience agent pipeline cancellations because fullsend requires `on_new_commit: none` for reliable agent runs. If you see unexpected pipeline cancellations, set `on_new_commit: none` in your `.gitlab-ci.yml` workflow block.

2. Delete all CI/CD variables prefixed with `FULLSEND_`. If you set `OPENAI_API_KEY` for the static-key route, delete it yourself too if you want it gone — fullsend never created it (it is a plain CI/CD variable, not `FULLSEND_`-prefixed) and does not delete it as part of uninstall
3. Confirm that project access tokens created for this repo — `fullsend-bot`, `fullsend-poller`, `fullsend-analyst`, `fullsend-coder`, and any `fullsend-role-*` tokens — were revoked. `repos uninstall` revokes them automatically when it can list project access tokens; if uninstall failed on token revocation, retry it, then revoke any that remain from Settings → Access Tokens. A 403 from the token-list API fails uninstall closed rather than being treated as "nothing to revoke" — GitLab returns the identical error for plan-tier feature gating, group-level PAT disablement, and insufficient token permissions, so silently continuing risks leaving tokens live after a reported-successful uninstall. Only a confirmed-gone project (404) is treated as an empty inventory. If the project access token API is genuinely and permanently unavailable for this project (for example, a plan tier that doesn't offer it), retrying will not converge — finish the teardown by revoking any remaining tokens by hand from Settings → Access Tokens, then run `fullsend repos uninstall "$PROJECT_PATH" --manifest-only` to drop the manifest entry once cleanup is confirmed
4. If you installed using a personal-token fallback or enrollment (`--gitlab-bot-token`/`FULLSEND_GITLAB_BOT_TOKEN` or `--gitlab-role-token` — see [Configuring GitLab § Free-tier bot token](configuring-gitlab.md#free-tier-bot-token) and [Role identities and GitLab Free](configuring-gitlab.md#role-identities-and-gitlab-free)), also revoke those personal PATs on the accounts that issued them (User Settings → Access Tokens, or Group Access Tokens if group-scoped). Deleting the CI/CD variables in step 2 does not revoke the underlying PATs — they remain valid until revoked directly on their issuing accounts.
5. Delete fullsend pipeline schedules (`fullsend slash poll` and `fullsend event poll`)
6. If you provisioned the shared `gitlab-oidc` WIF provider (see [Configuring GitLab § Inference Setup](configuring-gitlab.md#inference-setup)), revoke this repo's trust — `fullsend inference deprovision` does **not** cover `gitlab-oidc`; it only removes GitHub-style per-repo providers. Deleting the `FULLSEND_GCP_WIF_PROVIDER` CI/CD variable in step 2 does not revoke the underlying GCP IAM trust. What to do next depends on which install recipe you used:
   * **Default single-repo, or [multiple specific projects](configuring-gitlab.md#authorizing-multiple-specific-projects-alternative)** (a per-repo `attribute.project_path` principalSet): remove this repo's IAM binding — `gcloud projects remove-iam-policy-binding "$GCP_PROJECT" --role="roles/aiplatform.user" --member="principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/fullsend-inference/attribute.project_path/$PROJECT_PATH"`. This alone fully revokes `roles/aiplatform.user` for the repo. For hygiene, also drop this repo's `assertion.project_path == '...'` clause from the shared `--attribute-condition` (or narrow/delete the condition and provider entirely if no other GitLab repo shares them), so a stale CI job elsewhere can't even request a token against the provider.
   * **[Group or group tree](configuring-gitlab.md#authorizing-a-group-or-group-tree-alternative)** (a shared `attribute.namespace_path` principalSet): there is no per-repo IAM member to remove. A single repo cannot be carved out of group-wide trust while sibling repos in the namespace still need it — removing the `attribute.namespace_path/$GROUP_PATH` principalSet or narrowing the `--attribute-condition` revokes Vertex AI access for every project in the namespace, not just the one being uninstalled. To retire trust for just this repo, first migrate the namespace to the [multiple specific projects recipe](configuring-gitlab.md#authorizing-multiple-specific-projects-alternative) (one principalSet per remaining repo), then remove this repo's binding as described above.

If you manage your own self-hosted mint, run `fullsend mint unenroll "$OWNER/$REPO"` to remove the repo from the mint's allowlist. See the [standalone commands](#standalone-commands) table for details.

## Standalone commands

For organizations that separate GCP and GitHub responsibilities across teams, fullsend provides standalone commands that let each team run only the steps they own:

| Role | Command | What it does |
|------|---------|-------------|
| GCP Admin (Inference) | `fullsend inference provision <org\|owner/repo>` | Create WIF pool/provider and grant Agent Platform access (idempotent — safe to re-run for new orgs) |
| GCP Admin (Inference) | `fullsend inference deprovision <org\|owner/repo>` | Remove org or repo from WIF |
| GCP Admin (Inference) | `fullsend inference status <org\|owner/repo>` | Check WIF health, print config values |
| Repo Maintainer (OpenAI) | `fullsend inference openai request <owner/repo>[,...]` | Generate the provider/mapping request for an OpenAI organization admin (GPT on pi or codex) |
| Repo Maintainer (OpenAI) | `fullsend inference openai import [reply.json]` | Record the admin's reply in `config.yaml`, or set the repository variables |
| Repo Maintainer (OpenAI) | `fullsend inference openai status <owner/repo>` | Check the OpenAI WIF identifiers, and the exchange when run inside Actions |
| GitHub Maintainer | `fullsend github setup <org\|owner/repo>` | Configure GitHub org or repo (no GCP needed) |
| GitHub Maintainer | `fullsend github enroll <org> [repo...]` | Add repositories to agent enrollment |
| GitHub Maintainer | `fullsend github unenroll <org> [repo...]` | Remove repositories from agent enrollment |
| GitHub Maintainer | `fullsend github set <org\|owner/repo> <key> <value>` | Update a single config value (secret or variable) |
| GitHub Maintainer | `fullsend github status <org>` | Analyze GitHub-side installation state |
| GitHub Maintainer | `fullsend github sync-scaffold <org>` | Update workflow templates to current CLI version |
| GitHub Maintainer | `fullsend github uninstall <org>` | Remove GitHub configuration (org-level only) |
| GCP Admin (Mint) | `fullsend mint deploy` | Deploy the token mint Cloud Function |
| GCP Admin (Mint) | `fullsend mint delete` | Tear down mint infrastructure (inverse of deploy) |
| GCP Admin (Mint) | `fullsend mint add-role <role>` | Register a role PEM and app ID on the mint |
| GCP Admin (Mint) | `fullsend mint remove-role <role>` | Remove a role from the mint (deletes PEM secret by default) |
| GCP Admin (Mint) | `fullsend mint enroll <org\|owner/repo>` | Register an org or repo in the mint (does not grant Agent Platform access — use `inference provision`) |
| GCP Admin (Mint) | `fullsend mint unenroll <org\|owner/repo>` | Remove an org or repo from the mint |
| GCP Admin (Mint) | `fullsend mint status` | Inspect mint state and PEM health |

| Fleet Admin | `fullsend repos migrate <org> --project <gcp-project>` | Migrate an org from per-org to per-repo install, generating a `repos.yaml` manifest |
| Platform Admin | `fullsend repos install [repos...]` | Converge repos to desired state: provision new, repair component drift (workflow, thin callers, variables, secrets, pipeline schedules), repair scaffold content drift, refresh a declared configuration preset, upgrade refs |
| Platform Admin | `fullsend repos uninstall <repos...>` | Tear down fullsend from repos and remove from manifest |
| Fleet Admin | `fullsend repos status` | Compare manifest against actual per-repo state: detect missing or drifted components, ref drift, scaffold content drift, and declared configuration-preset drift |
| Fleet Admin | `fullsend repos set-default <key> <value>` | Set or remove a platform-level default in the manifest |

| Developer | `fullsend agent new <name>` | Generate a complete custom agent and register it |
| Developer | `fullsend agent add <url-or-path>` | Register an agent in config (URL auto-pinned to commit SHA) |
| Developer | `fullsend agent list` | List registered agents and their sources |
| Developer | `fullsend agent set <name>` | Set an agent's runtime, model or effort |
| Developer | `fullsend agent update <name> [sha]` | Re-pin a URL agent or a local harness `base:` URL to a new commit SHA |
| Developer | `fullsend agent remove <name>` | Unregister an agent from config |

The typical handoff for self-managed mints: a GCP admin runs `mint deploy` + `mint enroll` + `inference provision`, then passes the mint URL and WIF provider resource name to a GitHub maintainer who runs `github setup --mint-url=... --inference-wif-provider=...`. For the hosted community mint, enrollment is automatic — install the shared Apps and use the CLI defaults.

### Per-command IAM role breakdown

When using the split-responsibility workflow, each standalone command requires a subset of IAM roles. Use this table to request only what you need.

| IAM Role | `inference provision` | `inference deprovision` | `inference status` | `mint deploy` | `mint delete` | `mint add-role` | `mint remove-role` | `mint enroll` | `mint unenroll` | `mint status` |
|----------|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| `roles/iam.workloadIdentityPoolAdmin` | x | x | | x | x | | | x | x | |
| `roles/resourcemanager.projectIamAdmin` | x | | | \* | | | | | | |
| `roles/iam.serviceAccountAdmin` | | | | x | x | | | | | |
| `roles/secretmanager.admin` | | | | \* | x | \*\* | \*\*\* | | | |
| `roles/cloudfunctions.developer` | | | | x | x | | | | | |
| `roles/cloudfunctions.viewer` | | | | | | x | x | x | x | x |
| `roles/run.admin` | | | | x | | x | x | x | x | |
| `roles/iam.workloadIdentityPoolViewer` | | | x† | | | | | | | |
| `roles/secretmanager.viewer` | | | | | | § | | | | x |

\* `roles/resourcemanager.projectIamAdmin` and `roles/secretmanager.admin` are required for `mint deploy` only when using `--pem-dir` (first-time bootstrap). Standard deploys without `--pem-dir` do not need these roles.

\*\* `roles/secretmanager.admin` is required for `mint add-role` when uploading a new PEM (`--pem` or browser mode). When using `--use-existing-pem-secret`, only `roles/secretmanager.viewer` is required (see §).

\*\*\* `roles/secretmanager.admin` is required for `mint remove-role` unless `--keep-pem` is passed (default deletes the PEM secret).

§ `roles/secretmanager.viewer` is required for `mint add-role` when using `--use-existing-pem-secret` (checks that the PEM secret exists).

† All commands that call GCP APIs also require `resourcemanager.projects.get` (typically available via `roles/browser` or any project-level viewer role). This is only notable for `inference status` where it is not covered by the other listed roles.

Enrollment (org- or repo-scoped) does not grant IAM bindings — Vertex AI access is provisioned separately via `inference provision`.

Required GCP APIs also differ by command group:

```bash
# Inference commands (inference provision/deprovision/status):
gcloud services enable \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  aiplatform.googleapis.com \
  --project="$GCP_PROJECT"

# Mint commands (mint deploy/enroll/unenroll/status):
gcloud services enable \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  cloudfunctions.googleapis.com \
  run.googleapis.com \
  secretmanager.googleapis.com \
  iamcredentials.googleapis.com \
  --project="$GCP_PROJECT"
```

> **Note:** `iamcredentials.googleapis.com` is a runtime dependency — the deployed mint Cloud Function uses it for WIF token exchange, not the CLI itself. It must be enabled before `mint deploy`.

## Status notifications

See [Status Notifications](../user/customizing-agents.md#status-notifications) for configuring start/completion comments and reactions.

The composite action accepts five optional inputs for status notifications:

| Input | Description |
|-------|-------------|
| `run-url` | URL of the CI/CD run shown in the status comment |
| `status-repo` | Repository (`owner/repo`) to post status comments on |
| `status-number` | Issue or PR number for status comments |
| `status-comment-id` | ID of the comment that triggered a slash-command run; when set, reactions target that comment instead of the issue/PR |
| `mint-url` | URL of the token mint service used to obtain fresh tokens for posting comments |

All reusable workflows pass these inputs automatically.

### GitLab CI

On GitLab CI, the agent reads status notification context from standard CI/CD environment variables:

| Variable | Description |
|----------|-------------|
| `FULLSEND_FORGE_TOKEN` | **Required for the generated scaffold.** Protected project or group access token; `repos install` provisions it automatically. The agent job exports it internally as `GITLAB_TOKEN` for status-notification tooling. |
| `CI_SERVER_URL` | GitLab instance URL (set automatically by GitLab CI). Fallback when `FULLSEND_GITLAB_URL` and `GITLAB_API_URL` are unset. |
| `CI_COMMIT_SHA` | Commit SHA shown in the status comment. |
| `CI_MERGE_REQUEST_SOURCE_BRANCH_SHA` | Preferred over `CI_COMMIT_SHA` in merge request pipelines. |
| `CI_PIPELINE_ID` | Used as the run ID for status comment markers. |
| `CI_MERGE_REQUEST_IID` | When set, status comments target the merge request notes API instead of issues. |
| `FULLSEND_GITLAB_URL` | Override for `GITLAB_API_URL` and `CI_SERVER_URL` (e.g., for self-hosted instances). |
| `FULLSEND_NOTE_TARGET` | Set to `merge_requests` to force MR note targeting when `CI_MERGE_REQUEST_IID` is unavailable (e.g., child pipelines, scheduled jobs). |
| `CI_SERVER_TLS_CA_FILE` | GitLab Runner predefined path to a job-local PEM CA bundle when `tls-ca-file` is set. Consumed by poll/agent jobs and the GitLab Go client. See [Private CA](#private-ca-self-hosted-gitlab). |

For a generated scaffold, `repos install` provisions the protected
`FULLSEND_FORGE_TOKEN` variable (requested as masked when GitLab accepts the
value); the agent job exports it internally as `GITLAB_TOKEN` for status
notifications. If wiring fullsend into GitLab CI manually, provision
`FULLSEND_FORGE_TOKEN` as a masked and protected variable instead. See
[Configuring GitLab](configuring-gitlab.md#verifying-the-installation).

## Private CA (self-hosted GitLab)

Self-hosted GitLab instances that terminate TLS with a corporate or private CA need that CA in two **separate** places. A path that exists in the CI job container is not automatically present on a sandbox host.

Fullsend never disables TLS verification (`GIT_SSL_NO_VERIFY`, `curl -k`, or Go `InsecureSkipVerify`). Untrusted certificates are rejected. Installations that do not set a custom CA continue to use the public trust store.

### Job containers (poll and agent)

GitLab Runner injects `CI_SERVER_TLS_CA_FILE` when `tls-ca-file` is set in the runner `config.toml`. That file is a job-local PEM bundle. Generated poll and agent jobs source `.gitlab/ci/scripts/trust-ci-server-ca.sh` before their first GitLab network operation, and the GitLab Go client also loads the same variable, so curl, git, and `fullsend` all trust the CA without per-tool configuration. Public CAs stay in the pool: the extra PEM is appended, not used as a replacement.

Administrator contract:

1. Install the corporate CA on the **runner** (the process that talks to GitLab and starts jobs), not only on nodes that happen to run other workloads.
2. Point the runner at that bundle with [`tls-ca-file`](https://docs.gitlab.com/runner/configuration/tls-self-signed/) so GitLab Runner both verifies the GitLab server and sets `CI_SERVER_TLS_CA_FILE` in the job.
3. Keep the generated `.gitlab/ci/fullsend-*.yml` templates (re-run `repos install` to converge). Do not unset or override `CI_SERVER_TLS_CA_FILE` as a pipeline variable.

On the Kubernetes executor, `tls-ca-file` is still the contract. How the PEM gets onto the runner (host bind, ConfigMap volume, cluster-wide proxy CA) is an infrastructure choice; OpenShift-specific injection notes live with [#7406](https://github.com/fullsend-ai/fullsend/issues/7406). A volume mount of the CA into the job pod is not a substitute for `tls-ca-file` unless GitLab Runner also sets `CI_SERVER_TLS_CA_FILE`.

If `CI_SERVER_TLS_CA_FILE` is set but the file is missing, unreadable, or not a PEM certificate bundle, the job and the Go client fail with a diagnostic naming that variable. Leave it unset on public-CA instances (including gitlab.com).

Local `fullsend poll` / `fullsend run --forge gitlab` against a private-CA instance can set `CI_SERVER_TLS_CA_FILE` to a readable PEM path; the GitLab client will append it to the system pool.

### Sandbox hosts

OpenShell sandboxes do **not** inherit `CI_SERVER_TLS_CA_FILE`. That path is job-local and must not be treated as available inside the sandbox or on a remote gateway host. Agent-controlled TLS environment overrides (`SSL_CERT_FILE`, `SSL_CERT_DIR`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`) stay blocked.

The OpenShell supervisor reads a fixed list of system CA paths in the sandbox container to build upstream trust (GitLab, registries, inference) and the bundle handed to sandboxed processes. Administrators provision that trust on the **sandbox host**, independently of the job container:

- **fullsend GitLab Runner VMs** (`hack/gitlab-runner-vm/`): `setup.sh` installs the host CA (`install_ca_certs`) and an OCI `createRuntime` hook (`install_ca_hook`) that copies the host trust bundle into every container rootfs before PID 1 starts. That hook is specific to the Podman custom executor on those VMs. The Kubernetes job executor does not use it.
- **Other sandbox hosts** (including a gateway used by the Kubernetes executor): install the corporate CA in the host trust store (and any equivalent OCI hook or image) so the supervisor can verify GitLab. Do not copy a job-container path into the sandbox configuration.

A successful sandboxed agent run against the private-CA GitLab instance is the end-to-end check: poll jobs reach `/user`, agent jobs reach the GitLab API, and git/curl inside the sandbox reach GitLab through the supervisor's upstream trust.

## See Also

- [Getting Started](../getting-started/) — Standard per-repo installation
- [Advanced setup](../infrastructure/advanced-setup.md) — Alternative installation paths, setup flags, custom app sets
- [Mint service administration](../infrastructure/mint-administration.md) — Deploying and managing the token mint
- [Infrastructure Reference](../infrastructure/infrastructure-reference.md) — Token mint, WIF, and secrets deployment details
- [CLI Internals](../dev/cli-internals.md) — Command structure and implementation details
