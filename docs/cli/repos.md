---
sidebar_label: fullsend repos
---

# fullsend repos

Manage per-repo installations across multiple orgs via a declarative `repos.yaml` manifest. Compare the manifest's desired state against actual forge state and report installation status and configuration drift.

## Global flags

These flags are inherited by all `repos` subcommands:

| Flag | Default | Description |
|------|---------|-------------|
| `--gitlab-token` | | GitLab personal or project access token (overrides `GITLAB_TOKEN` env var) |

## Commands

| Command | Description |
|---------|-------------|
| `fullsend repos install [repos...]` | Converge repos to the desired state defined in a manifest |
| `fullsend repos uninstall <repos...>` | Tear down fullsend from repos and remove from manifest |
| `fullsend repos status` | Compare manifest against actual repo state |
| `fullsend repos set-default <key> <value>` | Set or remove a platform-level default in repos.yaml |

The former `repos migrate <org>` command (per-org to per-repo migration) has been removed along with per-org installation.

## `repos install`

Converge repos to the desired state defined in a manifest. This is the primary command for managing per-repo installations — it handles adding repos to the manifest, provisioning new repos, repairing component drift (workflow, thin callers, variables, secrets, pipeline schedules, GitLab poller protected-ref pipeline access — a disabled GitLab schedule is reported as drift and reactivated only when `--reactivate-schedules` is passed; typed GitLab jobs require verified `no_one_allowed` before template delivery, even with `FULLSEND_GITLAB_PIPELINE_VAR_RESTRICTION=enforced` — see [GitLab Role-Credential Contract](../contributing/gitlab-role-credentials.md#how-a-job-selects-its-credential)), repairing scaffold content drift (including structural rewrites of `.gitlab/ci/fullsend-pipeline.yml` at an unchanged template ref, and removal of a leftover `.gitlab/ci/fullsend-dispatch.yml` from installs predating #7707), and upgrading scaffold refs.

GitLab version pins require an upstream GitHub client to fetch matching
templates; unavailable templates are an error, not an embedded fallback.
GitLab vendor mode is rejected because its installer does not run a matching
vendored binary. Legacy variable-based wrappers retain their compatible
project setting. Typed activation migrates managed schedules off pipeline
variables while preserving disabled schedules and unrelated user settings.

When the manifest file does not exist and positional repo arguments are
provided, `repos install` bootstraps a new manifest (`version: 1`),
adds the specified repos, and writes the file. The `--forge` flag is
required in this case, and so is `--inference-auth` (a new manifest has no
inherited [inference authentication selection](#inference-authentication-selection)).
This enables a greenfield setup without manually creating the YAML first.

Runs in two phases:

1. **Manifest add** — repos specified as positional arguments that are not already in the manifest are added (`--forge` is required when the target platform cannot be inferred). Per-repo overrides (`--vertex-region`, `--fullsend-ref`, `--mint-url`, `--app-set`, `--allowed-remote-resources`, `--runtime`, `--inference-auth`) are written to the manifest entry. When `--inference-auth` is omitted and neither the forge section nor `defaults` selects an inference authentication method, install fails before the manifest is written.
2. **Converge** — all manifest repos are converged through a unified probe → diff → apply pipeline. Repos whose shim workflow is not yet on the default branch are freshly installed (scaffold files, variables, secrets, a declared configuration preset as `.fullsend/config.base.yaml`, and a canonical managed `.fullsend/config.yaml` when the repository is managed) onto the initialization branch (`fullsend/scaffold-install`). That includes a re-run while the initialization PR/MR is still open: variables and secrets may already exist from the first run, but the installer still updates the same initialization PR rather than opening a competing upgrade PR. Repos whose workflow is already on the default branch are checked for drift (workflow, thin callers, variables, secrets, pipeline schedules, GitLab poller protected-ref pipeline access — repaired automatically, except a disabled GitLab schedule, which is reported as drift and reactivated only with `--reactivate-schedules`; typed GitLab dispatch's required pipeline-variable restriction and managed schedule-variable migration), scaffold content drift (repaired automatically, including structural rewrites of `.gitlab/ci/fullsend-pipeline.yml` at an unchanged template ref, and removal of a leftover `.gitlab/ci/fullsend-dispatch.yml` from installs predating #7707), declared configuration-preset drift against `.fullsend/config.base.yaml` (replaced wholesale; `.fullsend/config.yaml` is preserved), managed `.fullsend/config.yaml` drift (replaced wholesale for managed repositories; unmanaged files are left untouched), and scaffold ref drift (upgraded automatically).

`defaults.config` and per-repository `config` declare a sparse typed managed configuration for `.fullsend/config.yaml` ([ADR 0122](../ADRs/0122-declarative-repo-configuration.md)). They share the per-repo config schema except `runtime` and `allowed_remote_resources`, which remain the existing manifest shorthands — putting either key inside `config` fails validation. `defaults.config` opts every repository in; a repository `config` (including `config: {}`) opts in only that repository. Unknown fields fail validation. This is not `config_base`, which copies a preset to `.fullsend/config.base.yaml`. Every managed file carries an ownership marker; a pre-existing `.fullsend/config.yaml` that lacks the marker is reported by `repos status` as "managed configuration (adoption required)" rather than ordinary drift, and install/convergence leave it untouched until it is adopted (manually edited to carry the marker, or replaced with the rendered managed body). Once a file carries the marker, install writes the canonical sparse file, `repos status` reports whole-file differences as drift, and convergence rewrites it. Repositories with neither declaration keep their existing file and are excluded from these checks. See [Repo Management — Managed configuration](../guides/getting-started/repo-management.md#managed-configuration). Before any write of a managed file, install and converge also compare the candidate and current effective configurations through the full runtime accessor chain (`kill_switch`, `roles`, `allowed_remote_resources`, agent `enabled: false` suppressions, and `create_issues.allow_targets`). A less-restrictive candidate is rejected unless the manifest explicitly declares that relaxation; status and install output identify the affected keys.

```yaml
version: 1
defaults:
  runtime: pi
  config:
    kill_switch: false
github:
  repos:
    - name: acme/api            # opted in by defaults.config
    - name: acme/special
      config:
        kill_switch: true       # repository values win
    - name: acme/unmanaged      # would be unmanaged without defaults.config
```

```bash
fullsend repos install -f repos.yaml
fullsend repos install --dry-run
fullsend repos install acme/api acme/web
fullsend repos install "acme/*" --direct --concurrency 8
fullsend repos install acme/new-repo --forge github --inference-auth vertex-wif --direct
```

When repos are specified as positional arguments, only those repos are processed. Glob patterns (e.g. `acme/*`) are matched against manifest entries. When no repos are specified, all manifest repos are converged. Credentials are required only for the forges of the selected repos: a GitLab-only selection does not need `GH_TOKEN`, and a GitHub-only selection does not need `GITLAB_TOKEN`. An unfiltered run still requires credentials for every forge present in the manifest.

### Inference authentication selection

Every repo that `repos install` converges or `repos status` checks must resolve an explicit inference authentication method, `inference.auth`. Three values are accepted:

| Value | Meaning |
|-------|---------|
| `vertex-wif` | Vertex AI through GCP Workload Identity Federation |
| `openai-api-key` | OpenAI through an API key |
| `openai-wif` | OpenAI through Workload Identity Federation (GitHub only; see [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md)) |

`inference.auth` can be set under `defaults`, in a forge section (`github` / `gitlab`), and on repo or glob entries. Resolution goes entry → forge section → `defaults`, using the normal entry matching rules: an explicit entry wins over a glob, and among globs the first match wins. A value on one entry never affects sibling repos. Only the selection is stored. Credentials, GCP values, and secret references do not belong under `inference`, and unknown keys there fail validation.

```yaml
version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  repos:
    - name: acme/api
    - name: acme/openai-svc
      inference:
        auth: openai-api-key
gitlab:
  url: https://gitlab.example.com
  inference:
    auth: openai-api-key        # every GitLab repo unless its entry says otherwise
  repos:
    - name: group/project
```

There is no implicit default. When no level selects a method, `repos install` (convergence) and `repos status` report a per-repo configuration error: `no inference authentication selected for <repo>`. The error names the levels where `inference.auth` can be set. Install reports it before any forge mutation for that repo, and other repos in the run still converge. An invalid value fails manifest validation. `repos uninstall` does not need the setting and does not validate its value, so a missing or invalid selection never blocks teardown.

`--inference-auth` takes the same three values (`openai-wif` is GitHub only). It has the highest precedence, because it is persisted as `inference.auth` on each selected manifest entry:

- **New entries** (repos added by this command) always get the flag value pinned on the entry, even when it matches the inherited value. Without the flag, a new entry carries no `inference.auth` of its own and inherits from the forge section or `defaults`. If neither provides a value, install fails before writing the manifest.
- **Existing entries** selected by the positional arguments (or every entry, in both forge sections, when no repos are given) are updated in place. A concrete repo that is matched only by a glob entry gets its own explicit entry, copied from that glob, so its siblings keep their selection.
- `defaults`, forge sections, and unselected entries are never changed. `--dry-run` applies the selection in memory only.

Because the override is kept at repo scope, later `repos status` and `repos install` runs resolve the same desired state without the flag. To change a forge-wide or global selection, use [`repos set-default`](#repos-set-default) with `github.inference.auth`, `gitlab.inference.auth`, or `defaults.inference.auth`.

#### Upgrading existing manifests

Older manifests have no `inference.auth`, and they are **not** treated as Vertex. Until a selection is added, `repos install` and `repos status` report every affected repo as misconfigured. Before upgrading, add the method each repo actually uses. For example, to keep the previous Vertex WIF behaviour for every repo:

```bash
fullsend repos set-default defaults.inference.auth vertex-wif
```

Or scope it to one forge or one repo:

```bash
fullsend repos set-default gitlab.inference.auth openai-api-key
fullsend repos install acme/api --inference-auth openai-api-key
```

You can also edit `repos.yaml` by hand and add `inference: {auth: ...}` at the level you want.

### Inference credentials

Each repo's effective `inference.auth` decides which credentials `repos install` provisions and converges for it:

| `inference.auth` | Secrets / CI/CD variables | Inputs |
|------------------|---------------------------|--------|
| `vertex-wif` | `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER` (secrets) and `FULLSEND_GCP_REGION` (variable) | `--vertex-project`, `--vertex-region`, plus `--vertex-wif-provider` when the project number cannot be derived |
| `openai-api-key` | `FULLSEND_OPENAI_API_KEY` (GitHub secret or masked GitLab CI/CD variable) | `--openai-api-key` |
| `openai-wif` | None required. `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER` and `FULLSEND_GCP_REGION` are written only when Vertex inputs are supplied, for Vertex sub-agents | Optional `--vertex-project`, `--vertex-region`, `--vertex-wif-provider` |

- **Per repo, before any write.** Every selected repo is validated against its own method before anything is written. A repo whose method's secrets are missing, with no inputs supplied for that method, fails with an error that names the repo and the parameters to supply. Inputs for one method are accepted when any selected repo uses it; repos with another method ignore them, so a mixed fleet can be installed in one run.
- **Reuse and replacement.** When no inputs are supplied for a method, existing secrets are reused unchanged, and re-running is a no-op (including while an initialization PR/MR is open). Supplied inputs replace the existing values. On GitLab, an existing `FULLSEND_OPENAI_API_KEY` is reused only when it is a masked, protected environment variable (not a file-type variable) with the wildcard `*` environment scope; otherwise the repo fails without echoing the value, so repair the variable or supply `--openai-api-key` to replace it.
- **No GCP lookups for OpenAI-only repos.** OpenAI-only repos get no GCP secrets or region variable. The GCP project number is looked up at most once, and only when a `vertex-wif` repo needs a derived WIF provider. The per-repo WIF provider derivation is unchanged.
- **Changing `inference.auth`.** Install writes the new credentials first and only after every convergence step for the repo succeeded deletes the Fullsend-managed secrets of the other method (`FULLSEND_GCP_PROJECT_ID`/`FULLSEND_GCP_WIF_PROVIDER` or `FULLSEND_OPENAI_API_KEY`). If any write fails, the old credentials are kept. The cleanup is attempted on every run once the selected method is established, so a deletion that failed earlier is retried by the next run. GitLab's unprefixed `OPENAI_API_KEY` variable is never deleted.
- **`--openai-api-key` value.** Surrounding whitespace is trimmed. On GitLab the key must be storable as a masked CI/CD variable (at least 8 characters from `A-Z a-z 0-9 _ + = / @ : . ~ -`, no whitespace); otherwise the repo fails instead of the key being stored unmasked.
- **Dry run.** `--dry-run` lists the secrets that would be written or deleted, by name only. It reports credentials that would be kept when the replacement is not yet live or inherited-variable scopes cannot be verified. Values are never printed.
- **`openai-wif` identifiers.** An `openai-wif` repo needs a complete set of OpenAI WIF identifiers, resolved like the runtime: when any of the `FULLSEND_OPENAI_AUDIENCE`, `FULLSEND_OPENAI_IDENTITY_PROVIDER_ID` or `FULLSEND_OPENAI_SERVICE_ACCOUNT_ID` Actions variables is set (a repository variable wins over an organization variable of the same name, as in the workflow `vars` context), those three variables are the only source and must all be set. Otherwise `inference.openai` (`audience`, `identity_provider_id`, `service_account_id`) in `.fullsend/config.yaml` is used, each identifier falling back to `.fullsend/config.base.yaml`. The two sources are never mixed. Install accounts for the managed configuration and configuration preset it is about to deliver. A repo with no identifiers or a partial set fails before anything is written, with an error that names the missing identifiers (never their values). The reusable workflows must also forward these variables and must not require GCP secrets the repo will not have (`FULLSEND_GCP_PROJECT_ID` and `FULLSEND_GCP_WIF_PROVIDER`, unless the repo already has them or the install supplies them with `--vertex-project`). Non-vendored reusable workflows are read from the pinned `fullsend_ref`, or from the release-default ref when none is pinned; rendered shim and thin callers must grant `id-token: write` before installation can change variables, secrets, or scaffold files. Vendored reusable workflows are checked from the actual vendor source. An established installation with no ref or planned scaffold delivery must have compatible installed workflows. An unpinned OpenAI WIF installation converges its callers to the running release’s upstream ref, including migration from an existing `github setup` installation. Otherwise the install is rejected and an existing `FULLSEND_OPENAI_API_KEY` is kept.
- **`openai-api-key` and leftover identifiers.** An `openai-api-key` repo is rejected while OpenAI WIF identifiers are still configured for it (repository or organization variables, or an `inference.openai` block in `.fullsend/config.yaml` or `config.base.yaml`), because the runtime prefers WIF over the static key. Remove those user-owned variables or the configuration block before installing; install never deletes them. On GitLab, group and instance variables the caller cannot read (no group Owner or administrator access, or a personal namespace) cannot be verified and are not treated as errors, and a configuration block is ignored.
- **Switching to `openai-wif`.** `FULLSEND_OPENAI_API_KEY` is deleted only once a complete identifier set is live on the default branch. While the identifiers are still in an unmerged initialization PR, the key is kept, and a later run removes it. `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER` and `FULLSEND_GCP_REGION` are never removed from an `openai-wif` repo, so Vertex sub-agents keep working.
- **`openai-wif` is GitHub only.** The token exchange needs a GitHub Actions OIDC token. Selecting `openai-wif` for a GitLab repo (on its entry, in the `gitlab` section, or inherited from `defaults`) is rejected before anything is written.
- **`--openai-api-key` is command-line only.** It is never written to `repos.yaml` and never logged.

#### GitLab: `FULLSEND_OPENAI_API_KEY` replaces `OPENAI_API_KEY` (breaking)

GitLab CI now reads the OpenAI key only from the `FULLSEND_OPENAI_API_KEY` CI/CD variable. The Fullsend job maps it to `OPENAI_API_KEY` for `fullsend run`. There is no fallback: an unprefixed `OPENAI_API_KEY` CI/CD variable on its own no longer works and does not satisfy the install check or `repos status`, which reports `FULLSEND_OPENAI_API_KEY` as missing. Fullsend never deletes the unprefixed variable, not even on `repos uninstall`, because other jobs may use it. Running `fullsend run` locally still reads `OPENAI_API_KEY`.

To upgrade an `openai-api-key` GitLab project that used `OPENAI_API_KEY`:

1. Provision the prefixed variable. Either run `fullsend repos install group/project --openai-api-key "$KEY"`, or add a masked, protected `FULLSEND_OPENAI_API_KEY` CI/CD variable of type Variable (not File) in Settings → CI/CD → Variables.
2. Re-run `fullsend repos install` so the project gets the updated `.gitlab/ci/scripts/run-agent-job.sh`.
3. If no other job uses it, delete the old `OPENAI_API_KEY` variable yourself.

#### Vertex flags renamed (breaking)

The Vertex AI credential inputs of `repos install` now carry a `vertex-` prefix. The old names were removed with no aliases, so passing them fails with `unknown flag`. Update scripts and CI jobs that invoke `repos install`:

| Removed | Replacement |
|---------|-------------|
| `--inference-project` | `--vertex-project` |
| `--inference-wif-provider` | `--vertex-wif-provider` |
| `--inference-region` | `--vertex-region` |

The values, validation, WIF provider derivation, and the `FULLSEND_GCP_*` secret and variable names they write are unchanged. `--inference-auth` and `--openai-api-key` keep their names. Other command groups (`github setup`, `admin install`, `inference`) keep their `--inference-*` flags.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path or URL to repos.yaml manifest |
| `--dry-run` | `false` | Preview what would change without making modifications |
| `--concurrency` | `4` | Max parallel operations (1-32) |
| `--roles` | `triage,coder,review,fix,retro,prioritize` | Agent roles to install. On a fresh install of a repo with a declared configuration preset, the preset's own roles take effect instead of this default unless `--roles` is explicitly passed on the command line. |
| `--direct` | `false` | Push scaffold directly to default branch (skip PR) |
| `--vertex-project` | | Optional GCP project ID for Vertex inference (written as `FULLSEND_GCP_PROJECT_ID` secret) |
| `--vertex-wif-provider` | | Full WIF provider resource name (`projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}`); uses this provider for all repos instead of deriving per-repo providers. Project number is embedded in the path, so no auto-derivation is needed. |
| `--forge` | | Forge type for new repos (`github` or `gitlab`). Required when adding repos not already in the manifest; inferred from existing platform sections when unambiguous. |
| `--force` | `false` | Allow scaffold ref downgrades |
| `--reactivate-schedules` | `false` | Reactivate required GitLab pipeline schedules that exist but are disabled (leave disabled by default so off-system polling setups are not silently reverted) |
| `--vertex-region` | | Per-repo GCP inference region override (default: global when `--vertex-project` is set; install-time only, not stored in the manifest) |
| `--openai-api-key` | | OpenAI API key written as `FULLSEND_OPENAI_API_KEY` to selected repos whose `inference.auth` is `openai-api-key` (GitHub and GitLab). Command-line only: never written to `repos.yaml` and never logged. See [Inference credentials](#inference-credentials). |
| `--fullsend-ref` | | Per-repo fullsend workflow ref override |
| `--mint-url` | | Per-repo mint URL override |
| `--app-set` | | GitHub App set prefix (apps named `{app-set}-{role}`), persisted as the `FULLSEND_APP_SET` repository variable for selected repos and recorded as a per-repo manifest override. GitHub-only; rejected when combined with a GitLab install. Must be lowercase alphanumeric with optional hyphens, max 23 characters. Pass `none` to reset an inherited manifest default back to the built-in `fullsend-ai`. |
| `--allowed-remote-resources` | | Per-repo allowed remote resources override. Each entry must be a valid HTTPS URL prefix ending with a trailing slash (no double-encoded `%25` sequences). |
| `--runtime` | | Agent runtime (`claude`, `pi`, `codex`) recorded for repos this command adds; existing entries keep their `runtime` / `defaults.runtime` |
| `--inference-auth` | | Inference authentication method (`vertex-wif`, `openai-api-key`, or `openai-wif`; `openai-wif` is GitHub only) persisted as `inference.auth` on each selected manifest entry, new and existing. It overrides forge-section and `defaults` values for those repos and never changes `defaults`, forge sections, or unselected repos. Required for new repos when no forge-section or `defaults` value exists. See [Inference authentication selection](#inference-authentication-selection). |
| `--vendor` | `false` | Vendor binary, reusable workflows, actions, and agent content into each repo for offline CI. Can also be set via `defaults.vendor` or per-repo `vendor` in the manifest. By default, the binary is auto-resolved from `--fullsend-ref`; use `--fullsend-binary` or `--fullsend-source` to provide it explicitly. |
| `--fullsend-binary` | | Path to a pre-built Linux fullsend binary to upload when vendoring instead of auto-resolving (requires `--vendor`) |
| `--fullsend-source` | | Path to a fullsend source checkout for content and cross-compile instead of auto-detecting or fetching from GitHub (requires `--vendor`) |
| `--gitlab-url` | | GitLab instance URL (e.g. `https://gitlab.example.com`); sets `gitlab.url` in the manifest and implies `--forge=gitlab` when no forge is specified. Private-CA instances also need runner `tls-ca-file` / `CI_SERVER_TLS_CA_FILE` — see [Private CA (self-hosted GitLab)](../guides/getting-started/operations.md#private-ca-self-hosted-gitlab) |
| `--gitlab-role-registry` | | Path to administrator GitLab role registry JSON (custom roles: credential references and policy, never secret values). Written as the protected unmasked `FULLSEND_GITLAB_ROLE_REGISTRY` variable. |
| `--gitlab-role-token` | | Administrator-provided GitLab role PAT (`role=token`, repeatable) for free-tier enrollment or a custom `own` credential. Values are never logged. |
| `--rotate-gitlab-roles` | `false` | Force-rotate GitLab role credentials even if they are not near expiry. Auto-rotation of expiring, expired, revoked, or unverified own-credential roles runs during every `repos install`. |
| `--rotate-gitlab-role` | | Rotate a specific GitLab role (repeatable). Default is all own-credential roles that are due. A `reuse` role follows its target. |
| `--rotate-gitlab-trigger-token` | `false` | Force-rotate the GitLab webhook fast-path pipeline trigger token (`FULLSEND_TRIGGER_TOKEN`). The webhook is updated to the new token first; the previous token is revoked afterwards. |

### GitLab role credentials

For GitLab repos, `repos install` provisions the built-in Poller, Analyst, and Coder role credentials (`FULLSEND_GITLAB_*_TOKEN`) and any registered custom roles. Runtime and CI routing always select the registered role credential and fail closed if it is missing; the legacy `FULLSEND_FORGE_TOKEN` is never used, and there is no migration gate. Install does not remove a leftover legacy shared secret or revoke the `fullsend-bot` project token — a repository installed before the role-only rollout requires manual cleanup of those. Custom roles are registered with `--gitlab-role-registry`; a custom role may reuse another registered credential or enroll its own token via `--gitlab-role-token`. The same `repos install` run rotates any own-credential role whose project access token is expiring, expired, revoked, or unverified. `--rotate-gitlab-roles` force-rotates every own-credential role; `--rotate-gitlab-role=poller` limits the run to one role. A failed rotation leaves the previous secret in place. There is no public rollback to the shared token. See [gitlab-role-credentials.md](../contributing/gitlab-role-credentials.md) and [Configuring GitLab § Role identity model and credential lifecycle](../guides/getting-started/configuring-gitlab.md#role-identity-model-and-credential-lifecycle). Developer is sufficient because poller state lives on dedicated unprotected branches rather than Maintainer-only CI/CD variables. Creating project access tokens requires GitLab Premium or Ultimate. The token expiry is computed in UTC so a local-timezone date cannot produce a token that GitLab already considers expired (`active: false`).

For GitLab repos, `repos install` also provisions the [ADR 0125](../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md) webhook fast path: a pipeline trigger token (`FULLSEND_TRIGGER_TOKEN`) and webhook secret (`FULLSEND_WEBHOOK_SECRET`), both stored as protected, masked CI/CD variables, plus a project webhook for issue, merge request, and comment events that triggers a pipeline on the protected default branch. The step is deferred (and reported) until the dispatcher is on the default branch, that branch is protected, and `ci_pipeline_variables_minimum_override_role=no_one_allowed` is verified. Re-runs are no-ops when everything is in place and repair a missing or drifted webhook or token. `--rotate-gitlab-trigger-token` mints a new token, updates the webhook, and then revokes the old token. Token, secret, and webhook URL values are never printed.

Install-time administration and runtime access are separate. Creating webhooks and trigger tokens requires a Maintainer (or Owner) credential, which `repos install` uses only transiently to provision and revoke resources; it never becomes a runtime identity. A pipeline trigger token runs pipelines with the permissions of the user who owns it, so the token is a persistent runtime credential and must not be owned above Developer. After minting a token, and whenever an existing managed token is reused or converged, `repos install` looks up the owner's effective project role (including group-inherited membership). A token whose owner is a Maintainer or Owner, or whose owner cannot be identified or looked up, is rejected: the token is revoked, the managed webhook is removed, the reason is reported, and the fast path stays disabled while the polling schedules continue. When the owner is only rejected (not unverifiable) and cleanup succeeds, this is a nonfatal deferral rather than a failed repository; owner-lookup and cleanup failures are still reported as errors. An owner at Developer level must also hold push or merge access to the protected default branch, or the fast path is deferred. Webhook teardown and revocation attempt every cleanup whose ownership can be established, even when variable, token-scope, or webhook discovery fails, and report all failures together. GitLab binds a trigger token to the user who creates it and only Maintainers can create one, so a fast path whose token would be owned by the installing Maintainer is blocked by this runtime privilege requirement rather than worked around by raising the poller, dispatcher, or agent credentials. The runtime role credentials stay Developer-level, `ci_pipeline_variables_minimum_override_role` stays `no_one_allowed`, and dispatch continues to use typed inputs.

Developer (30) access also depends on the default branch's protection settings: the poller creates pipelines via the API (`CreatePipeline`), which requires merge or push access to the protected default branch (see [ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md)). GitLab's default "Protected" preset grants Developers merge access, so this works out of the box. When a repo restricts both merge and push to Maintainers (or otherwise excludes Developer), `repos install` grants the poller project-access-token user (`fullsend-poller`) merge access — not push — on the protected default branch so the poller can create pipelines without widening Developer-class merge policy. If that grant is not possible (no poller token user id, or the GitLab API rejects the protection update), install fails closed with a remediation error instead of leaving dispatch silently broken. `repos status` reports `protected-ref-pipeline` drift when that access is later removed or tightened. If the permission gap reappears anyway, a `CreatePipeline` 403 now fails the poll cycle after persisting retry state (the event is retried, then dropped after three failures) rather than reporting a healthy cycle with nothing dispatched.

Install and converge also provision `FULLSEND_DISPATCH_SECRET` (a masked, protected CI/CD variable used to HMAC-sign dispatch variables and poller state) and create two unprotected poll-state branches (`fullsend-poll-state-slash` and `fullsend-poll-state-events`) holding an initial signed `state.json`. Existing `FULLSEND_LAST_POLL_AT_*` / `FULLSEND_LABEL_STATE` / `FULLSEND_DISPATCHED_KEYS_*` / `FULLSEND_FAILED_KEYS_*` values are migrated into those documents when present; otherwise each branch is seeded with an empty signed baseline. Already-written branch state is left untouched. After migrating, converge deletes any still-present retired poll-state CI/CD variables; they are treated as known-retired by the orphan detector (no warnings) and are not re-seeded on install.

On free-tier or Community Edition instances where project access tokens are not available, enroll each required role with a personal access token using `--gitlab-role-token`:

```bash
fullsend repos install group/project --forge gitlab --gitlab-role-token poller=glpat-xxxxxxxxxxxx --gitlab-role-token analyst=glpat-yyyyyyyyyyyy --gitlab-role-token coder=glpat-zzzzzzzzzzzz
```

Project paths can include nested groups (e.g., `group/subgroup/project`):

```bash
fullsend repos install group/subgroup/project --forge gitlab --gitlab-role-token poller=glpat-xxxxxxxxxxxx --gitlab-role-token analyst=glpat-yyyyyyyyyyyy --gitlab-role-token coder=glpat-zzzzzzzzzzzz
```

### Common workflows

Converge all repos from a manifest (provision new, repair component drift, repair scaffold content drift, refresh a declared configuration preset, rewrite a drifted managed configuration file, upgrade refs):

```bash
fullsend repos install -f repos.yaml
```

Preview changes without modifying infrastructure:

```bash
fullsend repos install -f repos.yaml --dry-run
```

Add a new repo to the manifest and install it:

```bash
fullsend repos install acme/new-repo --forge github --inference-auth vertex-wif --direct
```

Switch one existing repo to OpenAI API key authentication (persisted on its manifest entry):

```bash
fullsend repos install acme/api --inference-auth openai-api-key
```

Install specific repos:

```bash
fullsend repos install acme/api acme/web
```

Add a GitLab repo and install it:

```bash
fullsend repos install group/project --forge gitlab --gitlab-url https://gitlab.example.com --inference-auth vertex-wif --direct
```

In `fullsend repos status --json`, `gitlab_roles_ready` is true only when the
base role diagnosis, built-in role readiness, and every registered-role mapping
are ready. GitLab status always evaluates role credentials; missing role
secrets are unconditionally reported as drift.

## `repos status`

Read-only comparison of the `repos.yaml` manifest against actual forge state. Reports installation status and configuration drift for each repo, including declared configuration-preset drift against `.fullsend/config.base.yaml` and managed configuration drift against `.fullsend/config.yaml`.

```bash
fullsend repos status
fullsend repos status -f path/to/repos.yaml
fullsend repos status --repo acme/api --repo acme/web
fullsend repos status --repo "acme/*" --json
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--manifest` | `-f` | `repos.yaml` | Path or HTTPS URL to manifest file |
| `--repo` | | | Filter to specific repos (repeatable, supports globs) |
| `--json` | | `false` | Emit JSON output instead of table |
| `--concurrency` | | `8` | Max parallel API calls |

### Output

**Table output** (default) shows per-repo status with columns:

- **REPO** — `owner/repo` name (GitLab repos with nested groups display as `group/subgroup/project`)
- **REF** — Current workflow ref. Named refs (tags, branches) display as-is (e.g., `v2.3.0`, `main`). When the ref is a commit SHA, shows a truncated 7-character SHA with the expected ref in parentheses (e.g., `6f8b968 (main)`).
- **STATUS** — `installed`, `not installed`, or `error`. A repo with no resolved [inference authentication selection](#inference-authentication-selection) is reported as `error` with a configuration message, and its forge state is not inspected.
- **DRIFT** — Fields that differ from the manifest, scaffold files whose template content has changed, orphan files or variables no longer in the managed set, or `none`

Inference credentials are checked against each repo's own effective `inference.auth`, so a mixed fleet is evaluated repo by repo (see [Inference credentials](#inference-credentials)):

- A missing secret or CI/CD variable of the selected method is reported as drift on that name (for example `FULLSEND_OPENAI_API_KEY` reported as `missing`). The other method's credentials are not required.
- An `openai-wif` repo reports drift on `identifiers` when no complete OpenAI WIF identifier set is live on the default branch (missing, or partially configured). Only identifier names are reported. The GCP secrets are optional and `FULLSEND_OPENAI_API_KEY` is reported as obsolete. Drift on `workflows` means an installed caller or its actual reusable-workflow target cannot forward WIF identifiers, obtain an OIDC token, or run with the available secrets. Status checks live targets, including vendored workflows, rather than assuming the manifest pin has landed.
- An `openai-api-key` repo on GitHub reports drift on `residual-identifiers` when OpenAI WIF identifiers (complete or partial) stay active in an organization variable it inherits or in `inference.openai` configuration on the default branch, since the runtime prefers them over the API key. Repository-scoped identifier variables are reported as orphans. Only names are reported. `repos install` rejects such a repo until the residual variables or `inference.openai` blocks are removed.
- A Fullsend-managed secret of the method that is **not** selected (`FULLSEND_GCP_PROJECT_ID` / `FULLSEND_GCP_WIF_PROVIDER` on an `openai-api-key` repo, or `FULLSEND_OPENAI_API_KEY` on a `vertex-wif` or `openai-wif` repo) is reported as obsolete drift (`expected absent`). So is a leftover `FULLSEND_GCP_REGION` variable on an `openai-api-key` repo. This drift is reported even when the repo is otherwise not installed. `repos install` removes these once the replacement configuration is on the default branch. Only presence is checked, so values are never read or printed.
- On GitLab, only `FULLSEND_OPENAI_API_KEY` satisfies the `openai-api-key` requirement. An unprefixed `OPENAI_API_KEY` CI/CD variable does not, and is neither reported nor touched. `FULLSEND_OPENAI_API_KEY` is always classified as Fullsend-managed, never as an orphan.

On self-managed GitLab, a non-admin token generally cannot inspect instance CI/CD variables. Inaccessible group or instance scopes are reported as unverified rather than blocking an otherwise valid static-key install. After switching to `openai-api-key`, obsolete inference credentials remain until all inherited scopes can be verified. Confirm no inherited `FULLSEND_OPENAI_*` identifiers override the key, then remove credentials that are no longer needed manually. Confirmed residual identifiers still block convergence. Status reports inherited identifier conflicts and unread scopes as readiness drift. File-type OpenAI identifier variables also block the static-key route, even with blank stored contents: GitLab exports a nonblank temporary file path.

GitLab variable lookups use the wildcard environment scope (`*`), which is visible to Fullsend jobs without a deployment environment. Fullsend-managed variables, including `FULLSEND_MINT_URL`, are already created at wildcard scope, so Fullsend installations require no migration. A variable visible in GitLab settings but absent to the CLI may have only a named environment scope: check `environment_scope` before treating it as deleted. Optional GitLab variables can be absent without status drift; Fullsend currently has no required GitLab repository variables. Reads, updates, and deletion all target `*`, preserving definitions of the same name in named environments. Readable inherited OpenAI identifiers with blank values do not block a static-key installation; the nearest group overrides ancestor and instance values, while unreadable scopes remain unverified.

Status is read-only and reports secret and variable names only, never their values.

For GitLab repos, table and JSON output also include per-role credential
lifecycle diagnostic lines for roles needing attention (`expiring`,
`expired`, `revoked`, `unverified`, or `overlapping`); roles that are
`ok` or `unconfigured` do not get a diagnostic line. Missing, expired, or
revoked role credentials are reported as `gitlab-role:<name>` drift.
Status also appends built-in
Poller/Analyst/Coder and registered-role readiness checks (secret presence,
capability contract, and job-to-identity mapping). When token inventory is
available, expired, revoked, or unverified role credentials also downgrade
readiness; the base status path has no inventory and does not apply that
lifecycle refinement. A present shared
`FULLSEND_FORGE_TOKEN` is not treated as a substitute for a missing
built-in role. Registered roles with no agent mapping are also reported as
not ready. These checks do not retire the shared
token. Status also reports `protected-ref-pipeline` drift when the poller
cannot create pipelines on the protected default branch (Developer merge/push
is absent and the poller user is not in `allowed_to_merge` / `allowed_to_push`).

**JSON output** (`--json`) returns the full `StatusResult` object with per-repo details and aggregate summary counts.

### Exit codes

The command returns a non-zero exit code when any repo has drift, is not installed, or encountered an error. This makes it suitable for CI checks.

### Authentication

Requires a GitHub token via `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`. For GitLab repos, set the `GITLAB_TOKEN` environment variable or pass `--gitlab-token` to the `repos` command group.

## `repos uninstall`

Tear down fullsend from the specified repos and remove them from the manifest. By default, the command tears down first (opening a PR to remove workflow files, then deleting variables and secrets via the API), then removes successfully-torn-down repos from the manifest. Partial failures leave the manifest entry intact so the user can retry.

File deletions (workflow YAML, `.fullsend/config.yaml`, and GitLab `.gitlab-ci.yml` unmerge) are delivered as a pull request unless `--direct` is set, matching `repos install`. Variable and secret deletions are API-only operations and always happen immediately. For GitLab repos, uninstall also deletes the `fullsend-poll-state-slash` and `fullsend-poll-state-events` branches (a missing branch is ignored so older installs still uninstall cleanly) after deleting the Fullsend-owned webhook fast-path project webhook (hooks Fullsend does not own are left untouched) and revoking its managed pipeline trigger token before any scaffold file is removed, and deleting the wildcard-scoped `FULLSEND_TRIGGER_TOKEN` and `FULLSEND_WEBHOOK_SECRET` variables (a failure there fails uninstall and leaves the manifest entry in place for retry), while continuing to delete the retired poll-state CI/CD variables, registry/rotation-state variables, built-in and custom `FULLSEND_GITLAB_*_TOKEN` secrets, and the corresponding `fullsend-poller` / `fullsend-analyst` / `fullsend-coder` / `fullsend-role-*` project access tokens. Uninstall does **not** delete a leftover `FULLSEND_FORGE_TOKEN` secret or revoke a matching `fullsend-bot` project access token — a repository installed before the role-only rollout requires manual cleanup of those. Token revocation is part of uninstall success: if listing or revoking the role project access tokens fails, uninstall fails and the manifest entry is left in place for retry. Reinstall and converge do not revoke credentials that are already distributed.

Uninstall PR delivery intentionally reuses the same branch as `repos install`/`converge` (`fullsend/scaffold-install`), since already-deployed per-repo shims only exclude that branch name from dispatch. **Known limitation:** if an install PR is still open on that branch when uninstall runs (or an uninstall PR is open when install/converge runs), the existing PR is updated with the new commit but its title and body are left unchanged — the PR may show an install-oriented title while its diff now removes files, or vice versa. Check the PR's diff, not just its title, before merging when install and uninstall run close together against the same repo.

GCP WIF pool/provider cleanup for GitHub repos is handled separately via `inference deprovision`. This does not cover GitLab's shared `gitlab-oidc` WIF provider — for GitLab repos, see [Operations § Per-repo teardown](../guides/getting-started/operations.md#per-repo-teardown) step 6 to revoke that repo's WIF trust.

When multiple repos are targeted (via globs or explicit bulk lists), the command prompts for confirmation unless `--yes` is set. Credentials are required only for the forges of the targeted repos. Uninstall does not require an `inference.auth` selection, so manifests without one can still be torn down.

Uninstall removes every Fullsend-managed inference credential, whatever the repo's `inference.auth` is (or whether it is set or valid at all): `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER`, and `FULLSEND_OPENAI_API_KEY`, plus the `FULLSEND_GCP_REGION` variable. Leftovers from an earlier method and partially installed repos are cleaned up the same way. A credential that is already absent is skipped, so re-running uninstall is safe. Uninstall never deletes an unprefixed `OPENAI_API_KEY` secret or CI/CD variable, or any other credential Fullsend does not manage. It also leaves the user-managed `FULLSEND_OPENAI_AUDIENCE`, `FULLSEND_OPENAI_IDENTITY_PROVIDER_ID` and `FULLSEND_OPENAI_SERVICE_ACCOUNT_ID` identifier variables in place. Remove `OPENAI_API_KEY` yourself if nothing else uses it.

```bash
fullsend repos uninstall acme/old-api
fullsend repos uninstall "acme/*" --yes
fullsend repos uninstall acme/old-api --dry-run
fullsend repos uninstall acme/old-api --manifest-only
fullsend repos uninstall acme/old-api --uninstall-only
fullsend repos uninstall acme/old-api --direct
```

For GitLab repos with nested group paths, use the full path:

```bash
fullsend repos uninstall group/subgroup/project
```

### Modes

| Flag | Teardown | Manifest removal |
|------|----------|------------------|
| *(default)* | Yes | Yes (only if teardown succeeds) |
| `--manifest-only` | No | Yes |
| `--uninstall-only` | Yes | No |

- **Default:** tear down + remove from manifest. Only repos whose teardown succeeds are removed from the manifest.
- **`--manifest-only`:** remove the manifest entry without tearing down the installation. Use when the repo is already deleted/transferred or was never successfully installed.
- **`--uninstall-only`:** tear down the installation but keep the manifest entry. Use for temporary teardown with intent to reinstall later.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path or URL to repos.yaml manifest |
| `--dry-run` | `false` | Preview what would be uninstalled without making changes |
| `--yes` | `false` | Skip confirmation prompt when multiple repos are targeted |
| `--direct` | `false` | Push file deletions to the default branch instead of opening a PR |
| `--concurrency` | `4` | Max parallel operations (1-32) |
| `--manifest-only` | `false` | Remove from manifest without tearing down |
| `--uninstall-only` | `false` | Tear down without removing from manifest |

## `repos set-default`

Set or remove a platform-level default in `repos.yaml`. An empty value removes the key. Creates the manifest with `version: 1` if the file does not exist.

```bash
fullsend repos set-default <key> <value>
fullsend repos set-default github.fullsend_ref v2.5.0
fullsend repos set-default github.mint_url ""   # removes the key
```

### Valid keys

| Key | Type | Description |
|-----|------|-------------|
| `defaults.allowed_remote_resources` | comma-separated HTTPS URL prefixes, each ending with `/` | URL prefixes allowed for remote resources (agents, policies, skills, plugins, profiles, providers, and base composition). Each entry must be a valid HTTPS URL ending with a trailing slash (no double-encoded `%25` sequences). |
| `defaults.runtime` | `claude`, `pi` or `codex` | Agent runtime written as each repo's `runtime:` at install; a per-entry `runtime` overrides it (`none` stops the chain) |
| `defaults.inference.auth` | `vertex-wif`, `openai-api-key`, or `openai-wif` | Global [inference authentication selection](#inference-authentication-selection); forge-section and per-entry `inference.auth` override it |
| `defaults.vendor` | `true` or `false` | Vendor fullsend binary and content into each repo for offline CI; per-entry `vendor` overrides it. Currently GitHub-only; GitLab CI templates do not yet reference the vendored binary. |
| `defaults.config_base.source` | local path or HTTPS URL | Configuration preset written as `.fullsend/config.base.yaml`; a per-entry `config_base.source` overrides it (`none` disables inheritance). A local path is resolved relative to `repos.yaml`'s directory and must not escape it; manifests loaded from an HTTPS URL must use an HTTPS preset URL. Fetch/validation semantics otherwise match `github setup --config`. |
| `defaults.config_base.sha256` | 64-character SHA-256 hex | Optional digest that validates the fetched preset; a per-entry `config_base.sha256` overrides it (`none` skips validation). Same semantics as `github setup --config-hash`. |
| `github.url` | URL | GitHub instance URL (default: `https://github.com`) |
| `github.mint_url` | URL | Token mint service URL (defaults to `https://mint.fullsend.sh` in public mode) |
| `github.mint_mode` | `public` or `private` | Controls the default mint URL: `public` defaults to `https://mint.fullsend.sh`; `private` requires an explicit `mint_url` (default: `public`) |
| `github.fullsend_ref` | ref string | Git ref to pin in scaffold workflow YAML |
| `github.inference.auth` | `vertex-wif`, `openai-api-key`, or `openai-wif` | Inference authentication selection for every GitHub repo; overrides `defaults.inference.auth`, and a per-entry value overrides it |
| `gitlab.url` | URL | GitLab instance URL |
| `gitlab.fullsend_ref` | ref string | Git ref to pin in scaffold CI template files |
| `gitlab.inference.auth` | `vertex-wif` or `openai-api-key` | Inference authentication selection for every GitLab repo; overrides `defaults.inference.auth`, and a per-entry value overrides it |
| `gitlab.agent_runner_tags` | comma-separated tags | CI runner tags for routing agent (data-plane) jobs |
| `gitlab.control_runner_tags` | comma-separated tags | CI runner tags for routing control-plane jobs (poll today). Independent of `gitlab.agent_runner_tags`; unset renders `tags: []` (untagged) |
| `gitlab.runner_tags` | comma-separated tags | Deprecated alias for `gitlab.agent_runner_tags`. Still accepted; rewrites persist `agent_runner_tags`. On-disk persistence happens on `repos set-default`, `repos install` (only when it appends new manifest entries), and `repos uninstall` (only when it removes entries) — not `repos converge`, which resolves the alias in memory for rendering but does not rewrite `repos.yaml` |

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path to repos.yaml |

### Examples

Select Vertex WIF inference authentication for every repo, and OpenAI API key authentication for GitLab repos:

```bash
fullsend repos set-default defaults.inference.auth vertex-wif
fullsend repos set-default gitlab.inference.auth openai-api-key
```

Set GitLab agent runner tags:

```bash
fullsend repos set-default gitlab.agent_runner_tags fullsend-agent
```

Set multiple agent runner tags:

```bash
fullsend repos set-default gitlab.agent_runner_tags "fullsend-agent,gpu-runner"
```

Route control-plane jobs (poll) onto a cheaper runner fleet.
`gitlab.control_runner_tags` is independent of `gitlab.agent_runner_tags`;
left unset, control-plane jobs render `tags: []` (untagged):

```bash
fullsend repos set-default gitlab.control_runner_tags fullsend-api
```

Remove agent runner tags:

```bash
fullsend repos set-default gitlab.agent_runner_tags ""
```

`gitlab.runner_tags` remains a deprecated alias that writes
`gitlab.agent_runner_tags`:

```bash
fullsend repos set-default gitlab.runner_tags fullsend-agent
```

This alias rewrite is not scoped to `set-default`: `repos install` (only
when it appends new manifest entries) and `repos uninstall` (only when it
removes entries) also drop the deprecated `gitlab.runner_tags` key and
persist `gitlab.agent_runner_tags`, even if you never ran `set-default`
yourself. `repos converge` resolves the alias in memory for rendering
scaffold files on every run, but it does not rewrite `repos.yaml` — a
manifest that still has `gitlab.runner_tags` on disk keeps parsing
correctly until a command that actually writes the manifest runs.
Tooling that parses `repos.yaml` directly outside `fullsend`'s own
commands should prefer `gitlab.agent_runner_tags` when present and treat
`gitlab.runner_tags` as deprecated.

Set the GitLab instance URL:

```bash
fullsend repos set-default gitlab.url https://gitlab.example.com
```

## See also

- [Getting Started](../guides/getting-started/) — Standard per-repo installation
- [Configuring GitLab](../guides/getting-started/configuring-gitlab.md) — GitLab getting-started guide
- [Operations](../guides/getting-started/operations.md) — Day-2 administration
- [CLI Internals](../guides/dev/cli-internals.md) — Command structure and implementation details
