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
| `fullsend repos set <path> <value>` | Set or remove any field in repos.yaml (replaces `repos set-default`) |

The former `repos migrate <org>` command (per-org to per-repo migration) has been removed along with per-org installation.

## `repos install`

```bash
fullsend repos install [repos...] [flags]
```

Converge repos to the desired state defined in a manifest. This is the primary command for managing per-repo installations — it provisions repositories declared in the manifest, repairs component drift (workflow, thin callers, variables, secrets, pipeline schedules, GitLab poller protected-ref pipeline access — a disabled GitLab schedule is reported as drift and reactivated only when `--reactivate-schedules` is passed; typed GitLab jobs require verified `no_one_allowed` before template delivery, even with `FULLSEND_GITLAB_PIPELINE_VAR_RESTRICTION=enforced` — see [GitLab Role-Credential Contract](../contributing/gitlab-role-credentials.md#how-a-job-selects-its-credential)), repairs scaffold content drift (including structural rewrites of `.gitlab/ci/fullsend-pipeline.yml` at an unchanged template ref), and upgrades scaffold refs.

`repos.yaml` is the single source of persistent, non-secret install settings and of repository membership. Install reads every setting from the manifest; its flags supply only credentials, local vendoring artifacts, and controls for one run (see [Flags](#flags)). To change a setting or add a repository, edit `repos.yaml` or use [`repos set`](#repos-set), then run install. Moving from `github setup` or from the removed configuration flags is covered in [Migrating to manifest-driven `repos install`](../guides/getting-started/migrating-from-github-setup.md).

GitLab version pins require an upstream GitHub client to fetch matching
templates; unavailable templates are an error, not an embedded fallback.
GitLab vendor mode is rejected because its installer does not run a matching
vendored binary. Legacy variable-based wrappers (for example, from a
`fullsend_ref` pinned before v0.46.0) are refused. Typed activation refuses
while a managed schedule still sets a pipeline variable, including a
leftover `FULLSEND_POLL_MODE`; remove it, because Fullsend no longer
migrates it automatically.

A missing manifest file is an error: install does not create one. Create it, and declare repositories, with [`repos set`](#repos-set) (the first edit writes `version: 1`):

```bash
fullsend repos set defaults.inference.auth vertex-wif
fullsend repos set 'github.repos["acme/api"]' '{}'
fullsend repos install acme/api --vertex-project "<gcp-project>"
```

Runs in two phases:

1. **Select** — the repositories to converge are chosen from the entries declared in the manifest (see [Selecting repositories](#selecting-repositories)). Positional arguments only filter declared entries; they never create, copy, or carve out entries. Every selected repository must resolve an [inference authentication selection](#inference-authentication-selection), and its [effective infrastructure values](#effective-infrastructure-values) must be consistent, or that repository fails before any write.
2. **Converge** — all selected repos are converged through a unified probe → diff → apply pipeline. Repos whose shim workflow is not yet on the default branch are freshly installed (scaffold files, variables, secrets, a declared configuration preset as `.fullsend/config.base.yaml`, and a canonical managed `.fullsend/config.yaml`) onto the initialization branch (`fullsend/scaffold-install`). That includes a re-run while the initialization PR/MR is still open: variables and secrets may already exist from the first run, but the installer still updates the same initialization PR rather than opening a competing upgrade PR. Repos whose workflow is already on the default branch are checked for drift (workflow, thin callers, variables, secrets, pipeline schedules, GitLab poller protected-ref pipeline access — repaired automatically, except a disabled GitLab schedule, which is reported as drift and reactivated only with `--reactivate-schedules`; typed GitLab dispatch's required pipeline-variable restriction; legacy variable-based wrappers are refused, and a leftover pipeline variable on a managed schedule is not repaired and must be removed manually), scaffold content drift (repaired automatically, including structural rewrites of `.gitlab/ci/fullsend-pipeline.yml` at an unchanged template ref), declared configuration-preset drift against `.fullsend/config.base.yaml` (replaced wholesale; `.fullsend/config.yaml` is preserved), managed `.fullsend/config.yaml` drift (replaced wholesale; an existing file without the ownership marker stops that repository before any writes until it is adopted, while the other selected repositories still converge), and scaffold ref drift (upgraded automatically). When no configuration preset resolves for a repo, the desired state is no `.fullsend/config.base.yaml`. An existing base file then stops that repo's fresh or repeat install, including `--dry-run`, before any writes, with an error telling you to declare a `config_base` source or remove the file. A declared preset that cannot be loaded (fetch failure, `sha256` mismatch, invalid content) fails only that repository: install reports the error in that repository's result, converges the other selected repositories, and exits non-zero. An HTTPS preset fetch that fails at the transport level (for example, a dropped connection reported as `EOF`) is retried once, and the error names the phase that failed (DNS, connect, TLS handshake, awaiting the response), the remote address, and the elapsed time.

`defaults.config`, `github.config`, `gitlab.config`, and per-repository `config`
declare a sparse typed managed configuration for `.fullsend/config.yaml` ([ADR
0122](../ADRs/0122-declarative-repo-configuration.md)). Each accepts every
per-repo config field, including `runtime` and `allowed_remote_resources`. Each
field resolves through code defaults, the selected `config_base` preset,
`defaults.config`, the repository's forge section `config` (`github.config` for
GitHub repositories, `gitlab.config` for GitLab ones), and then the repository
entry's `config` (glob entries included), using that field's normal layered
merge rule: a more specific scalar wins, nested maps merge key by key,
`allowed_remote_resources` unions a non-empty list with the inherited prefixes,
and an explicit empty list (for example `allowed_remote_resources: []`) replaces
the inherited value. Because the managed file is sparse and cannot record that
reset, manifest validation rejects a non-empty `allowed_remote_resources` list
declared in a more specific layer than an explicit empty one (for example
`defaults.config` `[]` with a non-empty `github.config` or repository list)
unless the most specific declared list is itself empty, which denies every
remote resource whatever the earlier layers say; declare the full list in a
single layer instead. Omitted values inherit and are not written into the
managed file. Every repository selected from the manifest has a managed
`.fullsend/config.yaml`; no `config` key is required. With no `config` declared,
install renders the canonical sparse file carrying only the ownership marker
(omitted values still inherit from a declared preset and the code defaults).
When the manifest is loaded from an HTTPS URL, a `# Source manifest: <url>`
comment follows the ownership marker, naming the manifest the file was rendered
from; the line is recorded only for recognized raw-file URLs of public source
hosts (`raw.githubusercontent.com`, `github.com/<owner>/<repo>/raw/...` and
`gitlab.com/.../-/raw/...`, with no port and only plain `[A-Za-z0-9._~-]` path
segments) and only the URL's scheme, host and path are written (userinfo, query
string and fragment are removed). For any other manifest URL the line is
omitted, since a credential in the path cannot be told apart from a directory
name; do not put a credential in the path of a manifest URL on a recognized
host. A manifest loaded from a local path records no source line. The line is
informational: drift detection and convergence ignore it, so switching between a
local path and a URL for the same manifest content (or changing the manifest
URL) is not reported as drift and does not rewrite the file; the line is updated
the next time a configuration change rewrites the file. Unknown fields and
invalid values fail validation with an error naming the section (for example
`github.config` or `github.repos[acme/api].config`). This is not `config_base`,
which copies a preset to `.fullsend/config.base.yaml`. Every managed file
carries an ownership marker; a pre-existing `.fullsend/config.yaml` that lacks
the marker is reported by `repos status` as "managed configuration (adoption
required)" rather than ordinary drift, and install skips that repository, with
no manifest or forge write for it and the file left untouched, until it is
adopted (manually edited to carry the marker, or replaced with the rendered
managed body). This applies even when the repository has no `config` key.
Install prints the adoption guidance (the proposed `repos.yaml` entry and the
setting-level difference) for each skipped repository, lists it as `FAILED` in
the summary, converges the other selected repositories, and exits non-zero;
`--dry-run` does the same. Other configuration preflight problems (an
unreadable, malformed or non-per-repo `.fullsend/config.yaml`, an existing
`.fullsend/config.base.yaml` with no declared preset or with invalid content,
and a selector that matches no declared repository) still stop the whole run
before any write. Once a file carries the marker, install writes the canonical
sparse file, `repos status` reports whole-file differences as drift, and
convergence rewrites it. On a first install with no overlay yet, install records
its one install-time default in the repository's `repos.yaml` entry before
rendering, so the manifest stays the source of truth: with no `config_base`
preset and no explicit `create_issues` declaration on GitHub it writes
`config.create_issues.allow_targets.repos: [fullsend-ai/fullsend,
fullsend-ai/agents]`; on GitLab it writes an explicit empty policy,
`config.create_issues.allow_targets: {}` (the target repository is always
allowed and is not listed). A glob-covered repository gets its own entry copied
from the glob for this writeback; `--dry-run` shows the change without writing.
If the manifest is an HTTPS URL or cannot be written, install fails before any
manifest write or forge write and prints the exact entry or `config` edit to
make in the manifest source, for repositories with an explicit entry or covered
by a glob. See [Repo Management — Managed
configuration](../guides/getting-started/repo-management.md#managed-configuration).
Before any write of a managed file, install and converge also compare the
candidate and current effective configurations through the full runtime accessor
chain (`kill_switch`, `roles`, `allowed_remote_resources`, agent `enabled:
false` suppressions, `create_issues.allow_targets`, `inference.gateway.url`, and
`inference.gateway.audience`). A less-restrictive candidate is rejected unless
the manifest explicitly declares that relaxation; a change to either
`inference.gateway` field, including a preset introducing a gateway where an
established installation has none configured, must be declared in the manifest;
status and install output identify the affected keys. Adding a `config_base`
preset to a repository that has no base file is compared the same way (an absent
base is an empty layer), even when its managed overlay is unchanged. A pristine
first install (no Fullsend installation, no overlay and no base) has no current
effective configuration to relax, so it inherits permissions such as
`create_issues` targets from its declared preset without a safety rejection. A
fresh install that adoption or the safety gate blocks stops before any forge
file, variable, secret or PR/MR write, in a dry run and a live run alike, and
fails with an error.

```yaml
version: 1
defaults:
  config:
    runtime: pi
    kill_switch: false
github:
  config:
    allowed_remote_resources:
      - https://example.com/agents/   # GitHub repositories only
  repos:
    - name: acme/api            # inherits defaults.config
    - name: acme/special
      config:
        kill_switch: true       # repository values win
    - name: acme/plain          # no config key: still managed (sparse, marker only)
```

```bash
fullsend repos install -f repos.yaml
fullsend repos install --dry-run
fullsend repos install acme/api acme/web
fullsend repos install "acme/*" --direct --concurrency 8
```

### Selecting repositories

With no positional arguments, install converges every repository declared in the manifest. Positional arguments filter that set. Each one can be:

- the exact name of a declared entry (`acme/api`);
- a concrete repository name covered by a declared glob entry (`acme/web`, covered by `acme/*`);
- a glob pattern (`"acme/*"`), matched against declared entries.

Selectors never add repositories. Every selector must match at least one declared repository, and the whole run fails before any manifest or forge write if any one does not, even when other selectors match: `repos install acme/api 'other/*'` fails for `other/*` when only `acme/*` is declared. An unrelated glob is never treated as declared merely because the manifest contains another glob. For a concrete name the error lists every undeclared selector and suggests the entry to add, for example `fullsend repos set 'github.repos["owner/repo"]' '{}'` or `fullsend repos set 'gitlab.repos["group/project"]' '{}'` (a GitLab entry needs `gitlab.url` first). Declare the repository, then re-run. A glob selector is checked against the repositories the declared entries resolve to, so a glob that lists no repository fails with a "matched no manifest entries" error. Live install, `--dry-run` and `repos status` select identically.

A concrete repository named on the command line that a glob entry covers is installed with that glob entry's settings, even when the org listing omits it (for example a fork or an archived repository). No explicit entry is added for it. To give it different settings, declare an explicit entry.

Credentials are required only for the forges of the selected repos: a GitLab-only selection does not need `GH_TOKEN`, and a GitHub-only selection does not need `GITLAB_TOKEN`. An unfiltered run still requires credentials for every forge present in the manifest.

Install writes to `repos.yaml` itself in only two cases: the first-install `create_issues` writeback described above, and, after a first install, the CLI's release written to `github.fullsend_ref` or `gitlab.fullsend_ref` when that key is unset. `--dry-run` writes neither.

On GitHub, a `fullsend_ref` pinned to an older release needs the CLI from that release: this CLI's shim passes `FULLSEND_GITHUB_RUNNER_SECRETS` ([ADR 0136](../ADRs/0136-runner-secrets-through-one-stored-secret.md)), and GitHub rejects the call when the older reusable workflow does not declare it.

### Removed configuration flags (breaking)

`repos install` no longer accepts flags that set persistent repository state. Passing one fails immediately with an error that names the `repos.yaml` key and shows a `fullsend repos set` example; nothing is read or written. Declare the value in the manifest instead:

| Removed flag | `repos.yaml` key | Example |
|---|---|---|
| `--roles` | `config.roles` (`defaults`, forge section, or entry) | `fullsend repos set defaults.config.roles '[triage, coder, review]'` |
| `--fullsend-ref` | `fullsend_ref` (`github` / `gitlab` section or entry) | `fullsend repos set 'github.repos["acme/api"].fullsend_ref' v1.2.3` |
| `--mint-url` | `mint_url` (`github` section or GitHub entry), or `config.mint_url` | `fullsend repos set github.mint_url https://mint.example.com` |
| `--app-set` | `app_set` (`github` section or GitHub entry) | `fullsend repos set github.app_set acme` |
| `--inference-auth` | `inference.auth` (`defaults`, forge section, or entry) | `fullsend repos set defaults.inference.auth vertex-wif` |
| `--vendor`, hidden `--vendor-fullsend-binary` | `vendor` (`defaults` or entry) | `fullsend repos set defaults.vendor true` |
| `--signoff` | `signoff` (`defaults`, forge section, or entry) | `fullsend repos set defaults.signoff true` |
| `--gitlab-url` | `gitlab.url` | `fullsend repos set gitlab.url https://gitlab.example.com` |
| `--vertex-region` | `config.inference.region` (or the preset's `inference.region`) | `fullsend repos set defaults.config.inference.region us-east5` |
| `--forge`, and adding repositories with positional arguments | A repository entry | `fullsend repos set 'github.repos["acme/api"]' '{}'` |

No equivalent configuration flags were added to `github setup` or elsewhere. Declare `config.inference.region` in `repos.yaml` to make the region managed. Until you do, install keeps the existing `FULLSEND_GCP_REGION` (for example one set with the removed `--vertex-region`) and writes `global` only where the variable is missing; see [Effective infrastructure values](#effective-infrastructure-values). See [Migrating to manifest-driven `repos install`](../guides/getting-started/migrating-from-github-setup.md) for step-by-step migration.

### Scaffold PR delivery

Without `--direct`, install delivers scaffold changes as a pull request. On
GitHub, a token that owns the repository or has write access pushes a
`fullsend/scaffold-install` branch to the repository and opens the PR there.
Any other user token delivers from a fork: install reuses an existing fork, or
creates one (interactive runs offer a choice; non-interactive runs fork by
default; fine-grained PATs push to the repository instead).

GitHub App installation tokens never fork. They push the branch and open the
PR in the target repository, so the App needs the **Contents**, **Pull
requests** and **Workflows** repository permissions set to read and write (the
scaffold includes files under `.github/workflows/`), and must be installed on
that repository. When GitHub refuses to create the branch, commit the files or
create the PR with a 403, install fails with an error that names the
permissions to grant as one possible cause. A policy restriction, such as SAML
enforcement or the repository setting that blocks Actions from creating pull
requests, can produce the same denial.

### DCO sign-off

Repositories that enforce the [Developer Certificate of Origin](https://developercertificate.org/) reject commits without a `Signed-off-by` trailer. `repos install` can add one to every scaffold commit it creates or updates — fresh installs, re-runs of an open initialization PR/MR, drift repair, and upgrades — on GitHub and GitLab, for both direct (`--direct`) and PR/MR delivery. The trailer names the user who owns the installing token, resolved from the forge's authenticated-user API (`Signed-off-by: Name <email>`). When the profile has no name, the login is used. When it has no public email, the forge's noreply address is used.

Enable it with a boolean `signoff` in `repos.yaml`, at any of three levels:

```yaml
version: 1
defaults:
  signoff: true              # every repo
github:
  signoff: false             # overrides defaults for GitHub repos
  repos:
    - name: acme/api         # inherits github.signoff (false)
    - name: acme/dco-repo
      signoff: true          # repo entry wins over its forge section
gitlab:
  repos:
    - name: group/project    # inherits defaults.signoff (true)
      signoff: false         # explicit false disables an inherited true
```

A repo entry (or the glob entry that matches the repo) takes precedence over its forge section (`github` or `gitlab`), which takes precedence over `defaults`. Signoff is off when no level sets it. There is no command-line override: the former `--signoff` flag was removed, so set the level you want with, for example, `fullsend repos set defaults.signoff true`.

When signoff is enabled for any selected repo, the identity is resolved once per forge before anything is committed. A missing identity, such as a GitHub App installation token that cannot read `/user`, or an identity without a name or email fails the install before any scaffold commit is made. `--dry-run` runs the same identity check and reports the trailer that would be added. When signoff is disabled, no trailer is added and no identity lookup is made. Uninstall commits (file deletions) do not add a trailer.

### Signed commits

The DCO trailer is message text; it is independent of the cryptographic signature a repository's rules may require. On GitHub, scaffold commits are created with the `createCommitOnBranch` GraphQL mutation, which GitHub signs on behalf of the authenticated user, so every commit that `repos install`, upgrades and `repos uninstall` make shows as **Verified** and is accepted by rulesets that require verified signatures — with a `gh auth` token, a fine-grained PAT or a classic PAT alike. The author is the user who owns the token and the committer is GitHub, as for commits made in the web interface; no signing key is involved on the operator's machine. See [ADR 0140](../ADRs/0140-sign-github-scaffold-commits-via-createcommitonbranch.md).

Two cases keep the Git Data REST API, where GitHub signs commits only for App installation tokens: a vendored install (`vendor: true`; the vendored binary is streamed from disk and needs an executable mode, which the mutation cannot set) and any file with a mode other than `100644`. With a user token those commits are unsigned, so a vendored install cannot target a repository whose rules require signed commits; the install stops with "the repository requires signed commits and this commit is not signed". Because the mutation cannot change modes, the executable bit is not a managed property of GitHub installs: new files are created as `100644` and an existing file's mode is left as it is. On GitHub Enterprise Server the mutation only produces signed commits when the site administrator has enabled web commit signing.

On GitLab, scaffold commits go through the Commits API, and GitLab treats commits created through the REST API as web-based commits. They are signed with the instance's signing key only where web-based commit signing is set up: on GitLab.com, when the group or project has [Sign web-based commits](https://docs.gitlab.com/user/project/repository/signed_commits/web_commits/) turned on (available since GitLab 18.9, generally available in 18.10); on GitLab Self-Managed and GitLab Dedicated, when the instance has commit signing for GitLab UI commits configured. GitLab's "Reject unsigned commits" push rule does not apply to commits created through the API.

### Inference authentication selection

Every repo that `repos install` converges or `repos status` checks must resolve an explicit inference authentication method, `inference.auth`. Three values are accepted:

| Value | Meaning |
|-------|---------|
| `vertex-wif` | Vertex AI through GCP Workload Identity Federation |
| `openai-api-key` | OpenAI through an API key |
| `openai-wif` | OpenAI through Workload Identity Federation (GitHub only; see [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md)) |

`inference.auth` can be set under `defaults`, in a forge section (`github` / `gitlab`), and on repo or glob entries. Resolution goes entry → forge section → `defaults`, using the normal entry matching rules: an explicit entry wins over a glob, and among globs the first match wins. A value on one entry never affects sibling repos. Only the selection is stored in this manifest-level `inference` key. Credentials and secret references do not belong there, and unknown keys there fail validation. Non-secret GCP values (`inference.project`, `inference.region`, `inference.wif_provider`, and on GitLab `inference.wif_audience` and `inference.service_account`) are declared in the managed configuration instead, for example `defaults.config.inference.region`; see [Effective infrastructure values](#effective-infrastructure-values).

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

There is no command-line selection: the former `--inference-auth` flag was removed. Set the method with [`repos set`](#repos-set) at the level you want, for example `defaults.inference.auth`, `github.inference.auth`, `gitlab.inference.auth`, or `'github.repos["acme/api"].inference.auth'`.

This readiness rule is deliberately stricter than `github setup`, which can scaffold a repository without any inference credentials. Install converges many repositories unattended, so it requires an explicit method for each one: it then provisions exactly that method's credentials, removes the other method's leftover Fullsend-managed secrets, and fails early instead of leaving agents that cannot reach a model. Every selected repository must resolve `inference.auth`, and that method's credentials must already exist on the repository or be supplied (by flags or by declared configuration; see [Inference credentials](#inference-credentials)).

#### Upgrading existing manifests

Older manifests have no `inference.auth`, and they are **not** treated as Vertex. Until a selection is added, `repos install` and `repos status` report every affected repo as misconfigured. Before upgrading, add the method each repo actually uses. For example, to keep the previous Vertex WIF behaviour for every repo:

```bash
fullsend repos set defaults.inference.auth vertex-wif
```

Or scope it to one forge or one repo:

```bash
fullsend repos set gitlab.inference.auth openai-api-key
fullsend repos set 'github.repos["acme/api"].inference.auth' openai-api-key
```

You can also edit `repos.yaml` by hand and add `inference: {auth: ...}` at the level you want.

### Effective infrastructure values

Install, `--dry-run`, and `repos status` derive each repository's infrastructure variables and secrets from the same values, so the committed configuration and the repository variables never silently disagree. The source is the repository's **effective configuration**: the managed configuration (repository entry `config` → forge section `config` → `defaults.config`) layered over the declared preset (`config_base`). An existing markerless `.fullsend/config.yaml` is not part of it; adopt its values into `repos.yaml` first.

| Value | Applies to | Source |
|-------|-----------|--------|
| `FULLSEND_MINT_URL` (variable) | GitHub repositories | The effective configuration's `mint_url`, when declared in the managed configuration or the preset. Otherwise the manifest `mint_url` / `mint_mode` resolution (public mode defaults to `https://mint.fullsend.sh`) |
| `FULLSEND_GCP_REGION` (variable) | `vertex-wif` and `openai-wif` repositories | The effective configuration's `inference.region` when declared (managed configuration or preset); a missing variable is then drift that install repairs, even on a repeat install without credential inputs. With none declared, an existing variable is kept as is, and `global` is written only where the variable is missing and Vertex credentials are being written |
| `FULLSEND_GCP_PROJECT_ID` (secret) | `vertex-wif`, and `openai-wif` when supplied | `--vertex-project`, else the effective configuration's `inference.project` |
| `FULLSEND_GCP_WIF_PROVIDER` (secret) | `vertex-wif`, and `openai-wif` when supplied | `--vertex-wif-provider`, else the effective configuration's `inference.wif_provider`, else derived from the project |
| `FULLSEND_GCP_SERVICE_ACCOUNT` (variable) | GitLab `vertex-wif` repositories | The effective configuration's `inference.service_account`. Written only when declared; the agent job then impersonates that service account. A missing or different variable is drift that install repairs. When not declared, an existing variable is obsolete and install deletes it |
| Agent job token audience (`.gitlab/ci/fullsend-agent.yml`) | GitLab `vertex-wif` repositories | The effective configuration's `inference.wif_audience`, rendered into the agent job's `id_tokens` `aud`. Unset keeps `aud: "fullsend"` |
| OpenAI WIF identifiers | `openai-wif` repositories | `FULLSEND_OPENAI_AUDIENCE` / `_IDENTITY_PROVIDER_ID` / `_SERVICE_ACCOUNT_ID` repository variables when set, else `inference.openai` from the managed configuration, the preset, or the existing configuration (see [Inference credentials](#inference-credentials)) |

- **Private mode.** `mint_mode: private` has no built-in mint URL. Declare it as `mint_url` (in the `github` section or on the entry), as `config.mint_url`, or in a `config_base` preset. A `config.mint_url` alone is enough; you do not have to repeat it as `github.mint_url`. Manifest validation does not load presets, so a repository whose URL comes only from a preset is checked when install, dry run, and status resolve its effective configuration, and a repository that still has no URL fails there.
- **Mint URL conflicts.** When the manifest's explicit `mint_url` (in the `github` section or on the entry) and a `mint_url` declared in the managed configuration or preset differ, install, dry run, and status report a conflict for that repository before any write. Make the values match or unset one, for example `fullsend repos set --unset github.mint_url`. A `mint_url` that only a remote `config_base` preset supplies (none in the managed configuration) and that differs from the manifest-resolved value is accepted only when the preset is pinned with `config_base.sha256`; otherwise the preset's owner could redirect the mint endpoint without a `repos.yaml` change. Pin the preset or declare `mint_url` in `repos.yaml`.
- **Region.** When the effective configuration declares `inference.region` (in `defaults.config`, a forge section's `config`, the entry's `config`, or the preset), the region is managed: `FULLSEND_GCP_REGION` is checked against the declared value whether or not it exists, so a changed value and a missing variable are both drift. Install repairs the drift, `--dry-run` previews the change, and `repos status` reports it. This holds on a repeat install that supplies no credential inputs, as well as on fresh installs and whenever Vertex credentials are written. When no region is declared, an existing `FULLSEND_GCP_REGION` is preserved as is, with no drift reported, the same way an undeclared `app_set` keeps an existing `FULLSEND_APP_SET`; `global` is written only where the variable is missing and Vertex credentials are being written (a fresh install or a credential write). `openai-api-key` repositories never get it, and an existing one is removed once the OpenAI key is established. Make the region managed with, for example, `fullsend repos set defaults.config.inference.region us-east5`.
- **Project and WIF provider.** A flag that disagrees with a declared `inference.project` or `inference.wif_provider` fails that repository before any write. Declared values count as supplied credentials: secret values cannot be read back to compare, so every install rewrites `FULLSEND_GCP_PROJECT_ID` and `FULLSEND_GCP_WIF_PROVIDER` from them. That lets a `github setup` installation whose project and provider lived only in layered configuration migrate without flags. A WIF provider (flag or declared) without a project is an error, and a declared `inference.wif_provider` requires a declared `inference.project` (`--vertex-project` alone does not satisfy it), so `repos status` and install agree. A project without a WIF provider derives the project number through the GCP Resource Manager API, which needs Application Default Credentials; declare `inference.wif_provider` or pass `--vertex-wif-provider` to skip the lookup. Existing secrets are reused only when neither a flag nor the effective configuration supplies a project.
- **Existing WIF provider (GitLab).** `inference.wif_audience` and
  `inference.service_account` let a GitLab `vertex-wif` repository use a
  WIF provider the installer does not own, such as one a platform team runs
  for a whole GitLab instance. Both require an explicitly supplied WIF
  provider (`--vertex-wif-provider` or `inference.wif_provider`); combining
  them with the derived per-project `gitlab-oidc` provider fails before any
  write. Declare `inference.wif_provider` so `repos status` can check them
  without the flag. `inference.service_account` must be a full email ending
  in `.iam.gserviceaccount.com`, and `inference.wif_audience` a single line
  without `$`, which GitLab would expand as a variable. Set them under
  `gitlab.config` or on `gitlab.repos` entries; either key in `github.config`
  or on a `github.repos` entry fails validation. Values a GitHub repository
  inherits from `defaults.config`, or that reach a GitLab repository whose
  `inference.auth` is not `vertex-wif`, are ignored there (format is still
  checked). Install makes no GCP calls to check them. When
  `inference.service_account` is not declared (or does not apply), an
  existing `FULLSEND_GCP_SERVICE_ACCOUNT` variable is obsolete: `repos status`
  reports it as drift (`--json` shows `expected absent`) and install deletes
  it, so removing the key returns the agent
  job to direct federation. See
  [Using an existing WIF provider](../guides/getting-started/configuring-gitlab.md#using-an-existing-wif-provider)
  for a walkthrough.
- **OpenAI WIF identifiers are not secrets.** They are never written as secrets or treated as credentials. The optional GCP pair for Vertex sub-agents on an `openai-wif` repository follows the project and WIF provider rules above.

Declared `inference.project` and `inference.wif_provider` values are committed to `repos.yaml` and to the repository's `.fullsend/config.yaml`. Pass `--vertex-project` / `--vertex-wif-provider` instead if you prefer to keep them out of committed files.

### Inference credentials

Each repo's effective `inference.auth` decides which credentials `repos install` provisions and converges for it:

| `inference.auth` | Secrets / CI/CD variables | Inputs |
|------------------|---------------------------|--------|
| `vertex-wif` | `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER` (secrets) and `FULLSEND_GCP_REGION` (variable) | `--vertex-project` or a declared `inference.project`, plus `--vertex-wif-provider` or a declared `inference.wif_provider` when the project number cannot be derived. The region follows the [region rules](#effective-infrastructure-values) |
| `openai-api-key` | `FULLSEND_OPENAI_API_KEY` (GitHub secret or masked GitLab CI/CD variable) | `--openai-api-key` |
| `openai-wif` | None required. `FULLSEND_GCP_PROJECT_ID` and `FULLSEND_GCP_WIF_PROVIDER` are written only when Vertex inputs are supplied, for Vertex sub-agents; `FULLSEND_GCP_REGION` follows the [region rules](#effective-infrastructure-values) | Optional `--vertex-project` / `--vertex-wif-provider`, or the declared equivalents |

- **Per repo, before any write.** Every selected repo is validated against its own method before anything is written. A repo whose method's secrets are missing, with no inputs supplied for that method (by flags or declared configuration), fails with an error that names the repo and the parameters to supply. Inputs for one method are accepted when any selected repo uses it; repos with another method ignore them, so a mixed fleet can be installed in one run.
- **Reuse and replacement.** When no inputs are supplied for a method, existing secrets are reused unchanged, and re-running is a no-op (including while an initialization PR/MR is open). Supplied inputs, including a declared `inference.project`, replace the existing values on every run. On GitLab, an existing `FULLSEND_OPENAI_API_KEY` is reused only when it is a masked, protected environment variable (not a file-type variable) with the wildcard `*` environment scope; otherwise the repo fails without echoing the value, so repair the variable or supply `--openai-api-key` to replace it.
- **No GCP lookups for OpenAI-only repos.** `openai-api-key` repos get no GCP secrets or region variable. The GCP project number is looked up only when a repo's project has no WIF provider from `--vertex-wif-provider` or a declared `inference.wif_provider`. The per-repo WIF provider derivation is unchanged.
- **Changing `inference.auth`.** Install writes the new credentials first and only after every convergence step for the repo succeeded deletes the Fullsend-managed secrets of the other method (`FULLSEND_GCP_PROJECT_ID`/`FULLSEND_GCP_WIF_PROVIDER` or `FULLSEND_OPENAI_API_KEY`). If any write fails, the old credentials are kept. The cleanup is attempted on every run once the selected method is established, so a deletion that failed earlier is retried by the next run. GitLab's unprefixed `OPENAI_API_KEY` variable is never deleted.
- **`--openai-api-key` value.** Surrounding whitespace is trimmed. On GitLab the key must be storable as a masked CI/CD variable (at least 8 characters from `A-Z a-z 0-9 _ + = / @ : . ~ -`, no whitespace); otherwise the repo fails instead of the key being stored unmasked.
- **Dry run.** `--dry-run` lists the secrets that would be written or deleted, by name only. It reports credentials that would be kept when the replacement is not yet live or inherited-variable scopes cannot be verified. Values are never printed.
- **`openai-wif` identifiers.** An `openai-wif` repo needs a complete set of OpenAI WIF identifiers, resolved like the runtime: when any of the `FULLSEND_OPENAI_AUDIENCE`, `FULLSEND_OPENAI_IDENTITY_PROVIDER_ID` or `FULLSEND_OPENAI_SERVICE_ACCOUNT_ID` Actions variables is set (a repository variable wins over an organization variable of the same name, as in the workflow `vars` context), those three variables are the only source and must all be set. Otherwise `inference.openai` (`audience`, `identity_provider_id`, `service_account_id`) in `.fullsend/config.yaml` is used, each identifier falling back to `.fullsend/config.base.yaml`. The two sources are never mixed. Install accounts for the managed configuration and configuration preset it is about to deliver. A repo with no identifiers or a partial set fails before anything is written, with an error that names the missing identifiers (never their values). The reusable workflows must also forward these variables and must not require GCP secrets the repo will not have (`FULLSEND_GCP_PROJECT_ID` and `FULLSEND_GCP_WIF_PROVIDER`, unless the repo already has them or the install supplies them with `--vertex-project` or a declared `inference.project`). Non-vendored reusable workflows are read from the pinned `fullsend_ref`, or from the release-default ref when none is pinned; rendered shim and thin callers must grant `id-token: write` before installation can change variables, secrets, or scaffold files. Vendored reusable workflows are checked from the actual vendor source. An established installation with no ref or planned scaffold delivery must have compatible installed workflows. An unpinned OpenAI WIF installation converges its callers to the running release’s upstream ref, including migration from an existing `github setup` installation. Otherwise the install is rejected and an existing `FULLSEND_OPENAI_API_KEY` is kept.
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

#### GitLab: comment body moved to `_normalized_event.transition.comment.body` (breaking)

The GitLab dispatch payload built by the poller and the webhook dispatcher no longer carries a top-level `note_body` field. The comment body now travels only at `_normalized_event.transition.comment.body`, and the scaffolded `.gitlab/ci/scripts/run-agent-job.sh` reads only that path for `/fs-fix` instructions and the retro comment. The old script and the new payload do not interoperate: `/fs-fix` runs dispatch successfully but start with no human instruction, and the retro agent receives an empty comment. Nothing reports an error, because the payload carries no version marker.

Upgrade the scaffold and the poller or webhook dispatcher CLI together:

1. Upgrade the `fullsend` CLI that runs the poller or webhook dispatcher. A pipeline that installs a floating version such as `FULLSEND_VERSION="latest"` picks up the new payload on its next run.
2. Re-run `fullsend repos install` for every GitLab project so each gets the updated `.gitlab/ci/scripts/run-agent-job.sh`.

The reverse skew behaves the same way: a new scaffold paired with an old poller finds no body at the canonical path and drops the instruction.

A `/fs-*` command body is also no longer truncated to 800 characters in transport. The dispatcher refuses to dispatch a command whose payload exceeds the chunk budget instead of running the agent on a shortened instruction. Other bodies, such as bot review notes, keep the 800-character bound and the truncation marker.

#### `runtime` and `allowed_remote_resources` moved into `config` (breaking)

`runtime` and `allowed_remote_resources` are no longer sibling keys under `defaults` or a repository entry. Loading a manifest that still uses `defaults.runtime`, `defaults.allowed_remote_resources`, or the repository-entry equivalents fails with an error naming the replacement path (for example `defaults.runtime is no longer supported; move it to defaults.config.runtime`). The old spellings are not migrated automatically and are not accepted as aliases. `repos install` no longer accepts `--runtime` or `--allowed-remote-resources`, and `repos set` rejects `defaults.runtime` and `defaults.allowed_remote_resources` with the replacement path; set `defaults.config.runtime` or `defaults.config.allowed_remote_resources` instead.

To upgrade, move each value under the matching `config` section, then re-run `fullsend repos install`:

```yaml
# Before (rejected)
defaults:
  runtime: pi
  allowed_remote_resources: []
github:
  repos:
    - name: acme/api
      runtime: claude

# After
defaults:
  config:
    runtime: pi
    allowed_remote_resources: []
github:
  repos:
    - name: acme/api
      config:
        runtime: claude
```

A repository-level `allowed_remote_resources` value now follows the per-repo config merge rule: a non-empty list is unioned with the prefixes inherited from `github.config` / `gitlab.config` and `defaults.config` (previously it replaced the defaults list). Because a repository can only add to inherited prefixes, it can no longer narrow them to a subset; only an explicit empty list (`allowed_remote_resources: []`) denies every remote resource.

#### Vertex flags renamed (breaking)

The Vertex AI credential inputs of `repos install` now carry a `vertex-` prefix. The old names were removed with no aliases, so passing them fails with `unknown flag`. Update scripts and CI jobs that invoke `repos install`:

| Removed | Replacement |
|---------|-------------|
| `--inference-project` | `--vertex-project` (or a declared `config.inference.project`) |
| `--inference-wif-provider` | `--vertex-wif-provider` (or a declared `config.inference.wif_provider`) |
| `--inference-region` | `config.inference.region` in `repos.yaml`. The interim `--vertex-region` flag was also removed; see [Removed configuration flags](#removed-configuration-flags-breaking) |

The validation, WIF provider derivation, and the `FULLSEND_GCP_*` secret and variable names are unchanged. `--openai-api-key` keeps its name; `--inference-auth` was removed in favor of the `inference.auth` manifest key. Other command groups (`github setup`, `admin install`, `inference`) keep their `--inference-*` flags.

### Flags

Install flags supply only secrets and sensitive provisioning inputs, local vendoring artifacts, and controls for one run. None of their values is written to `repos.yaml`. Persistent settings live in the manifest; the flags that used to set them were removed (see [Removed configuration flags](#removed-configuration-flags-breaking)).

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path or URL to repos.yaml manifest. The file must exist |
| `--dry-run` | `false` | Preview what would change without making modifications |
| `--direct` | `false` | Push scaffold directly to default branch (skip PR) |
| `--concurrency` | `4` | Max parallel operations (1-32) |
| `--force` | `false` | Allow scaffold ref downgrades |
| `--reactivate-schedules` | `false` | Reactivate required GitLab pipeline schedules that exist but are disabled (leave disabled by default so off-system polling setups are not silently reverted). A schedule the GitLab trigger account owns can be edited only by that account, so the trigger-account stage reactivates it by replacing it |
| `--vertex-project` | | GCP project ID for Vertex inference, written as the `FULLSEND_GCP_PROJECT_ID` secret. Optional when the effective configuration declares `inference.project`; a different value from the declared one fails that repo. See [Effective infrastructure values](#effective-infrastructure-values) |
| `--vertex-wif-provider` | | Full WIF provider resource name (`projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}`), written as the `FULLSEND_GCP_WIF_PROVIDER` secret instead of deriving per-repo providers. The project number is embedded in the path, so no lookup is needed. Optional when the effective configuration declares `inference.wif_provider`; a different value from the declared one fails that repo. Requires a project |
| `--openai-api-key` | | OpenAI API key written as `FULLSEND_OPENAI_API_KEY` to selected repos whose `inference.auth` is `openai-api-key` (GitHub and GitLab). Command-line only: never written to `repos.yaml` and never logged. See [Inference credentials](#inference-credentials). |
| `--fullsend-binary` | | Path to a pre-built Linux fullsend binary to upload when vendoring instead of auto-resolving it from the resolved `fullsend_ref`. Valid only when at least one selected repo resolves `vendor: true`; otherwise install fails and points at `fullsend repos set defaults.vendor true` |
| `--fullsend-source` | | Path to a fullsend source checkout for vendored content and cross-compilation instead of auto-detecting or fetching from GitHub. Same `vendor: true` requirement as `--fullsend-binary` |
| `--gitlab-role-registry` | | Path to administrator GitLab role registry JSON (custom roles: credential references and policy, never secret values). Written as the protected unmasked `FULLSEND_GITLAB_ROLE_REGISTRY` variable. |
| `--gitlab-role-token` | | Administrator-provided GitLab role PAT (`role=token`, repeatable) for free-tier enrollment or a custom `own` credential. Values are never logged. |
| `--rotate-gitlab-roles` | `false` | Force-rotate GitLab role credentials even if they are not near expiry. Auto-rotation of expiring, expired, revoked, or unverified own-credential roles runs during every `repos install`. |
| `--rotate-gitlab-role` | | Rotate a specific GitLab role (repeatable). Default is all own-credential roles that are due. A `reuse` role follows its target. |
| `--rotate-gitlab-trigger-token` | `false` | Force-rotate the GitLab webhook fast-path pipeline trigger token (`FULLSEND_TRIGGER_TOKEN`). The webhook is updated to the new token first; the previous token is revoked afterwards. While the Poller is a managed project service account, the new token is created by a fresh Poller generation; rotation defers and keeps the existing compliant token while a superseded Poller generation has not finished retiring, or when the project is ineligible (see [Poller-owned trigger token](#poller-owned-trigger-token)). While the webhook uses the trigger account's token, this flag rotates nothing; use `--rotate-gitlab-trigger-account`. |
| `--rotate-gitlab-trigger-account` | `false` | Replace the GitLab trigger-only service account. Rotation creates a fresh generation (new account, deploy key, protected-branch push entry, and trigger token published to `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN`) and retires the previous generation only after the replacement is published and the managed webhook cutover is verified. Needs a ready trigger account; otherwise install finishes it (see [Trigger-only service account](#trigger-only-service-account)). |

The `--gitlab-token` persistent flag (or `GITLAB_TOKEN`) supplies the GitLab credential; see [Global flags](#global-flags).

### Vendoring

`vendor: true` (under `defaults` or on a repository entry) copies the fullsend binary, reusable workflows, actions, and agent content into each repository for offline CI. By default the binary is auto-resolved from the repository's resolved `fullsend_ref`; `--fullsend-binary` and `--fullsend-source` provide it from local files for that run only. GitLab vendor mode is rejected. Enable vendoring with `fullsend repos set defaults.vendor true`, or per repository with `fullsend repos set 'github.repos["acme/api"].vendor' true`.

#### Turning vendoring off

To stop vendoring, set `vendor: false` in `repos.yaml`, under `defaults` or on a repository entry. The usual precedence applies: an explicit entry overrides a glob that covers the same repository. For example, run `fullsend repos set 'github.repos["acme/api"].vendor' false`, then run `repos install`. Install has no flag that overrides this setting.

When a GitHub repository resolves `vendor: false`, install removes the assets an earlier vendored install left behind. Those deletions go into the same scaffold commit as the other changes, whether that commit lands directly or through a pull request. Install deletes only files that Fullsend is known to have shipped. The vendor manifest is repository content that anyone can edit, so listing a path in it never makes the path deletable on its own:

- **Vendor manifest present.** If `.fullsend/vendor-manifest.yaml` exists, install removes only the entries it lists that are also on Fullsend's list of shipped files: files of the `.defaults/` mirror (also under `.fullsend/.defaults/`) from the current embedded layout or from an explicit list of paths earlier Fullsend versions shipped, Fullsend's reusable workflow files (`.github/workflows/reusable-dispatch.yml` and similar), the binary at `.fullsend/bin/fullsend`, and the manifest itself. Any other entry the manifest lists is kept and counted in the output, without its name. This covers a file you created under `.defaults/` or `.fullsend/.defaults/`, even in a directory Fullsend uses, as well as `.fullsend/config.yaml`, `.fullsend/config.base.yaml`, custom actions or workflows, and a root `action.yml`.
- **Binary only.** If there is no manifest but `.fullsend/bin/fullsend` exists, install removes only the binary, the files of the current embedded `.defaults/` layout, and Fullsend's reusable workflow files. It never removes a root `action.yml` or files under `.github/actions/` or `.github/scripts/`.
- **Neither present.** If there is no manifest and no binary, install removes nothing.
- **Errors.** A malformed or untrusted vendor manifest, a failed read, or a repository tree too large to list fails the repository before anything is written.

Vendored callers run Fullsend's reusable workflows from local `./.github/workflows/reusable-*.yml` targets. Before deleting those files, install rewrites Fullsend's own callers (the shim and the thin callers) in the same commit so they reference `fullsend-ai/fullsend/.github/workflows/<name>@<ref>`. Install then checks every resulting workflow in `.github/workflows/`, including workflows you wrote. If any of them still calls a local reusable workflow that would be removed, install keeps all the stale assets and reports a `pending` action that names the workflow. This happens with a custom caller. It also happens when neither the manifest's `fullsend_ref` nor a release build supplies a ref to render Fullsend's callers. To clear it, set `fullsend_ref`, use a release build, or point the custom caller at the upstream reusable workflow, then re-run install.

On an established installation, install reads the stale assets, reads the workflows that might call the local targets, renders Fullsend's callers and plans the cleanup before it writes any variable or secret. If any of those steps fails, the repository fails with no variable, secret, or scaffold writes. The same plan is then used for the scaffold commit, so a live install, `--dry-run` and `repos status` reach the same decision. This ordering applies to repositories with stale vendored assets. Install still stops a repository before any write to it when it needs adoption of a managed configuration or rejects a configuration for safety.

`--dry-run` previews the exact files to remove and rewrite without writing anything. [`repos status`](#repos-status) reports the same pending cleanup as drift on `vendored-assets`, including for partial installations. After the cleanup lands, a repeat install and `repos status` report nothing for vendored assets. GitLab repositories are not affected.

### GitLab role credentials

For GitLab repos, `repos install` provisions the built-in Poller, Analyst, and Coder role credentials (`FULLSEND_GITLAB_*_TOKEN`) and any registered custom roles. Runtime and CI routing always select the registered role credential and fail closed if it is missing; the legacy `FULLSEND_FORGE_TOKEN` is never used, and there is no migration gate. Install does not remove a leftover legacy shared secret or revoke the `fullsend-bot` project token — a repository installed before the role-only rollout requires manual cleanup of those. Custom roles are registered with `--gitlab-role-registry`; a custom role may reuse another registered credential or enroll its own token via `--gitlab-role-token`. The same `repos install` run rotates any own-credential role whose project access token is expiring, expired, revoked, or unverified. `--rotate-gitlab-roles` force-rotates every own-credential role; `--rotate-gitlab-role=poller` limits the run to one role. A failed rotation leaves the previous secret in place. `repos install` exits non-zero when any role credential rotation fails (reporting `GitLab role rotation incomplete: N role credential(s) pending`), when the project-token inventory is unavailable for a forced or role-filtered rotation or when `--gitlab-role-token` replacements are supplied, and when reconciling the Poller's elevated access fails; rerun the install after resolving the reported cause. There is no public rollback to the shared token. See [gitlab-role-credentials.md](../contributing/gitlab-role-credentials.md) and [Configuring GitLab § Role identity model and credential lifecycle](../guides/getting-started/configuring-gitlab.md#role-identity-model-and-credential-lifecycle). Developer is sufficient because poller state lives on dedicated unprotected branches rather than Maintainer-only CI/CD variables. Creating project access tokens requires GitLab Premium or Ultimate on gitlab.com; self-managed instances, including Community Edition, can create them without a paid tier. The token expiry is computed in UTC so a local-timezone date cannot produce a token that GitLab already considers expired (`active: false`).

For GitLab repos, `repos install` also provisions the
[ADR 0125](../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md) webhook fast
path: a pipeline trigger token (`FULLSEND_TRIGGER_TOKEN`) and webhook secret
(`FULLSEND_WEBHOOK_SECRET`), both stored as protected, masked CI/CD variables,
plus a project webhook for issue, merge request, and comment events that
triggers a pipeline on the protected default branch. The step is deferred (and
reported) until the dispatcher is on the default branch, that branch is
protected, and `ci_pipeline_variables_minimum_override_role=no_one_allowed` is
verified. The polling schedules stay in place as the backstop: they keep
dispatching while the fast path is deferred or disabled, and they recover events
whose webhook delivery is missed. See
[Least-privilege dispatch contract](../guides/getting-started/configuring-gitlab.md#least-privilege-dispatch-contract)
for how both paths dispatch at Developer level with typed inputs. The step is
also deferred when a project, group, or instance-level CI/CD variable redefines
a predefined variable the trigger-pipeline checks rely on (such as
`CI_PIPELINE_SOURCE` or `CI_COMMIT_REF_PROTECTED`). Listing instance-level
variables needs administrator access and listing group variables needs group
Owner access. Every scope the installing credential can read is checked. A scope
that answers forbidden is unverified, not unsafe: it neither defers a new fast
path nor revokes an existing one, so a project Maintainer can enable the fast
path without administrator or group Owner access. Other listing errors still
fail the step. Fullsend does not verify unreadable scopes, so it trusts instance
administrators and group Owners not to override those variables, either at
install time or later. See
[Inherited CI/CD variable trust](../guides/getting-started/configuring-gitlab.md#inherited-cicd-variable-trust)
for this assumption and its failure mode. Re-runs are no-ops when everything is
in place and repair a missing or drifted webhook or token.
`--rotate-gitlab-trigger-token` mints a new token, updates the webhook, and then
revokes the old token. While the trigger account's dispatch activation is
recorded (the account route), the webhook instead embeds
`FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN`, `FULLSEND_TRIGGER_TOKEN` and the
Poller-owned webhook trigger are retired, `--rotate-gitlab-trigger-token`
rotates nothing, and `--rotate-gitlab-trigger-account` rotates the credential
the webhook uses. Token, secret, and webhook URL values are never printed. The
fast-path provisioning and safety-reconciliation steps run under the
`FULLSEND_GITLAB_INSTALL_LEASE` project lease; when it cannot be taken they
change nothing and report the step as deferred together with an error (see
[Upgrade compatibility](#upgrade-compatibility)).

Project-wide trigger safety failures remove the published account token and
revoke its owned triggers through the ledger. This applies to any existing
trigger account that has published a credential, whether or not dispatch
activation is recorded, and on both the early containment and the install and
rotation paths, not only to an account on the webhook route. When containment
succeeds, `repos install` continues and the later stages defer, with the
account route inactive. When any removal or revocation fails, `repos install`
reports `GitLab trigger account recovery failed` with every error joined,
marks the repository as failed, skips its later stages, and exits non-zero.
`repos status` does not evaluate the project-wide safety invariants itself; it
reports the result of a containment as drift (the published trigger token was
removed or revoked) until `repos install` publishes a replacement. The
account lifecycle does not publish a replacement until those checks pass. If a
Poller role stage fails, a requested trigger-account rotation waits so the
existing webhook is not left pointing at a revoked token. The installer still
refreshes the webhook after ordinary account repair in that failure path when
the account route is active, because repair can replace its token. It does not
move an account webhook onto an unverified Poller route. The poller,
dispatcher, and agent job scripts unset both trigger-token variables before
running, and `fullsend run` excludes them from harness expansion.

Install-time administration and runtime access are separate. Creating webhooks and trigger tokens requires a Maintainer (or Owner) credential, which `repos install` uses only transiently to provision and revoke resources; it never becomes a runtime identity. A pipeline trigger token runs pipelines with the permissions of the user who owns it, so the token is a persistent runtime credential and must not be owned above Developer. After minting a token, and whenever an existing managed token is reused or converged, `repos install` looks up the owner's effective project role (including group-inherited membership). A token whose owner is a Maintainer or Owner, or whose owner cannot be identified or looked up, is rejected: the token is revoked, the managed webhook is removed, the reason is reported, and the fast path stays disabled while the polling schedules continue. When the owner is only rejected (not unverifiable) and cleanup succeeds, this is a nonfatal deferral rather than a failed repository; owner-lookup and cleanup failures are still reported as errors. On the Poller-owned route, an owner at Developer level must also hold user-level push or merge access to the protected default branch, or the fast path is deferred. The trigger-only account route instead uses its recorded deploy-key push grant. Webhook teardown and revocation attempt every cleanup whose ownership can be established, even when variable, token-scope, or webhook discovery fails, and report all failures together. GitLab binds a trigger token to the user who creates it and only Maintainers can create one, so a fast path whose token would be owned by the installing Maintainer is blocked by this runtime privilege requirement rather than worked around by raising the poller, dispatcher, or agent credentials. The runtime role credentials stay Developer-level, `ci_pipeline_variables_minimum_override_role` stays `no_one_allowed`, and dispatch continues to use typed inputs.

Developer (30) access also depends on the default branch's protection settings: the poller creates pipelines via the API (`CreatePipeline`), which requires merge or push access to the protected default branch (see [ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md)). GitLab's default "Protected" preset grants Developers merge access, so this works out of the box. When a repo restricts both merge and push to Maintainers (or otherwise excludes Developer), `repos install` grants the Poller's identity — its project service account or project-access-token user (`fullsend-poller`), or the owner of a supplied Poller credential — merge access, not push, on the protected default branch so the poller can create pipelines without widening Developer-class merge policy. If that grant is not possible (no poller token user id, or the GitLab API rejects the protection update), install fails closed with a remediation error instead of leaving dispatch silently broken. `repos status` reports `protected-ref-pipeline` drift when that access is later removed or tightened. If the permission gap reappears anyway, a `CreatePipeline` 403 now fails the poll cycle after persisting retry state (the event is retried, then dropped after three failures) rather than reporting a healthy cycle with nothing dispatched.

Install and converge also provision `FULLSEND_DISPATCH_SECRET` (a masked, protected CI/CD variable used to HMAC-sign dispatch variables and poller state) and create two unprotected poll-state branches (`fullsend-poll-state-slash` and `fullsend-poll-state-events`) holding an initial signed `state.json`. Each missing branch is seeded with an empty signed baseline (the events branch also records an initial issue-open recovery cutoff timestamp). Already-written branch state is left untouched. The retired `FULLSEND_LAST_POLL_AT_*` / `FULLSEND_LABEL_STATE` / `FULLSEND_DISPATCHED_KEYS_*` / `FULLSEND_FAILED_KEYS_*` CI/CD variables are no longer migrated: install and converge neither read nor delete them, the orphan detector treats them as known-retired (no warnings), and uninstall deletes any leftovers. Before any write, `repos install` (including `--dry-run`) reads the project's push rule branch name regex (**Settings > Repository > Push rules > Branch name**) and refuses the installation when it rejects `fullsend-poll-state-slash` or `fullsend-poll-state-events` and that branch does not already hold `state.json`; the error names the regex and the branches to allow. The check is skipped when the push rule cannot be read (for example on editions without push rules, or without permission to read it) or when the regex does not compile.

On free-tier or Community Edition instances where project access tokens are not available, enroll each required role with a personal access token using `--gitlab-role-token`. Declare the project in `repos.yaml` first (`fullsend repos set gitlab.url <url>`, then `fullsend repos set 'gitlab.repos["group/project"]' '{}'`):

```bash
fullsend repos install group/project --gitlab-role-token poller=glpat-xxxxxxxxxxxx --gitlab-role-token analyst=glpat-yyyyyyyyyyyy --gitlab-role-token coder=glpat-zzzzzzzzzzzz
```

Project paths can include nested groups (e.g., `group/subgroup/project`):

```bash
fullsend repos install group/subgroup/project --gitlab-role-token poller=glpat-xxxxxxxxxxxx --gitlab-role-token analyst=glpat-yyyyyyyyyyyy --gitlab-role-token coder=glpat-zzzzzzzzzzzz
```

### GitLab project service-account lifecycle

> **Status: active.** `repos install`, rotation, `repos status` and
> `repos uninstall` manage GitLab role identities as project service accounts
> ([#8242](https://github.com/fullsend-ai/fullsend/issues/8242)), and write
> rotation state in the versioned format described in
> [Upgrade compatibility](#upgrade-compatibility). Every operator who runs
> install or converge against a project must use a CLI with this change. The
> live fresh-identity Poller handoff
> ([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)) activates the
> webhook fast path where the project is eligible. Ineligible or ambiguous
> projects defer, and cron polling stays the dispatch path.

#### Install and reconciliation

On supported GitLab instances, normal install replaces positively identified
Fullsend-managed legacy role tokens with project service-account credentials.
It verifies each replacement before publishing it and retains outgoing tokens
for the normal rotation grace period. Administrator-supplied credentials are
preserved; no separate migration command is required.

A supplied token must satisfy the same policy as a credential Fullsend mints
for the role, checked by authenticating with it before anything is recorded or
published: it must be active and unrevoked, carry the role's token name (for
example `fullsend-coder`) and exactly the `api` scope, and have an expiry that
has not passed and is no later than one year from today (UTC). Its owner must
also hold exactly effective Developer access on the target project, including
inherited group membership, as the owner of a minted replacement does. A token
that fails the policy or the access check, or that cannot be authenticated or
verified (for example a revoked token, a 401 response or a GitLab outage), is
rejected with a per-role failure before any enrollment is recorded or secret is
published, and the installed credential and its recorded provenance are left in
place. A supplied credential the project inventory cannot
list (a personal access token) is authenticated with its installed value on
every unforced install or rotation: one that still authenticates and has not
expired, and whose owner still holds exactly Developer access, is retained; one
that is revoked, expired or empty is replaced as described above; and one that
cannot be verified (for example a GitLab outage, or an owner whose access has
since been elevated or cannot be read) is left untouched and reported as a role
failure or status drift to retry. A supplied Poller credential must also let its
owner create pipelines on the protected default branch: enrollment establishes
and rereads that access before publishing, and install and status include the
supplied Poller's owner in their protected-branch pipeline-access checks.

Passing `--gitlab-role-token <role>=<token>` for a role that has a recorded
managed credential takes priority over automatic service-account convergence.
Install skips the managed replacement for that role and enrolls the supplied
token over the managed credential, even when the managed credential is still
fresh. You do not need to delete the variable or force a rotation first. This
applies only to roles inside the `--rotate-gitlab-role` filter (expanded through
credential reuse); a supplied token for a role the filter excludes is not
processed. The unknown-provenance case below differs: there the supplied token
alone does not re-adopt the installed credential.

Enrolling a supplied token does not retire the role's earlier managed
credentials. Tokens that rotation state still tracks stay recorded, and they
remain usable until they expire or you revoke them; no later install revokes
them automatically while the supplied credential stays enrolled. Revoke any you
no longer want by hand. Enrollment also recovers a role whose recorded managed
service account no longer exists (for example, someone deleted it): when the
complete account inventory positively shows the account is absent, install
enrolls the supplied token instead of failing, and later installs and forced
rotations are not blocked by the missing account. An account inventory that
cannot be read still fails closed.

Install provisions project service-account PATs where supported; legacy project
access tokens are a fallback and require GitLab Premium or Ultimate on
gitlab.com; self-managed instances, including Community Edition, can create them
without a paid tier. The same
`repos install` run rotates any own-credential role whose service-account PAT or
legacy project access token is expiring, expired, revoked, or unverified. A
failed replacement validation leaves the previous secret in place. If a managed
account cannot be restored to verified Developer access, its managed PATs are
contained and only a secret attributed to that account is removed. When neither
project service-account PATs nor legacy project access tokens are available,
enroll each required role with a personal access token using
`--gitlab-role-token`, as shown above.

Reinstall reconciles each positively owned, non-supplied role account to direct
Developer membership and verifies effective Developer access before retaining
its credential. Inherited higher access or failed verification fails
provisioning. For positively owned, non-supplied accounts, failed membership
verification also revokes and verifies managed role PATs on a bounded cleanup
context independent of cancellation. The installed secret is removed only after
attributing it to that account; ownership records remain for retry. Unmanaged
credentials and incomplete containment require administrator recovery.

When the default branch excludes Developer from merge and push, the
protected-branch merge grant described above goes to the Poller service-account
user, or to the project-access-token user on the fallback path. An enrolled
supplied Poller's owner receives the same grant after its enrollment is
recorded and before its credential is published. If enrollment then fails (the
access reread or the publication fails), the install rolls back the grant it
added. GitLab identifies each protected-branch entry by its own ID, so only the
entry that did not exist before enrollment is removed, by that ID; the owner's
user ID is never used to remove access and an entry that existed before
enrollment (such as an administrator's grant) is never touched. Removal runs on a
bounded context that survives a cancelled run. A grant is kept, and reported in
the run diagnostics, when it cannot be safely removed: the access could not be
read before or after the failure, the entry carries no ID, removal fails, or the
credential write may have been committed and the installed credential still needs
the access. Remove a reported grant in **Settings → Repository → Protected
branches** if the credential will not be enrolled. A grant that existed before
enrollment is never reported.

Legacy GitLab role tokens without explicit creation records are preserved during
service-account convergence, grace cleanup, and uninstall. A matching token name
or a historical distribution record does not prove ownership. Install reports
manual recovery or explicit supplied-credential enrollment when creation
provenance is unverified. Version 2 rotation state stores positively created
token IDs in `created_token_ids`; older state without those records does not
authorize destructive legacy-token cleanup.

#### Re-adopting a role credential of unknown provenance

Install is fail-static for an installed role credential that rotation state
does not record (for example, one installed before rotation state existed). It
leaves every credential untouched and reports that supplied credential
ownership is unresolved, or that the credential should be re-enrolled with
`--gitlab-role-token`. While such a credential is installed, project-wide
account exclusions cannot be resolved, so service-account convergence and
rotation stop for the whole project, not only for that role. Install never
duplicates, revokes or adopts the credential on its own.

To re-adopt it, for **every** role reported in the same run:

1. In **Settings → CI/CD → Variables**, remove the role's credential variable
   (`FULLSEND_GITLAB_POLLER_TOKEN`, `FULLSEND_GITLAB_ANALYST_TOKEN`,
   `FULLSEND_GITLAB_CODER_TOKEN`, or `FULLSEND_GITLAB_ROLE_<NAME>_TOKEN`).
   Running jobs keep the copy they already hold.
2. Re-run `fullsend repos install`:
   - to keep the same credential, pass it once with
     `--gitlab-role-token <role>=<token>`. Install records it as
     administrator-supplied, and rotation manages its lifecycle from then on.
   - or, to let Fullsend take over, omit it. Install provisions a managed
     service-account credential. The previous token is not recorded as
     Fullsend-created, so it is never revoked automatically: revoke it
     manually once in-flight jobs have finished.

Passing `--gitlab-role-token` while the old variable is still installed does
not re-adopt it, because provisioning keeps present credentials and rotation
stops at the unresolved exclusions first.

#### Recovering a supplied credential recorded without an owner

Enrolling or rotating to an administrator-supplied credential records its owner
by authenticating with it. When that lookup fails, the role is recorded as
supplied without an owner, and the same state is kept when storing a replacement
fails after GitLab may already have accepted it. While the installed credential
authenticates, the next install or rotation attributes and records the owner
itself. If it expires, is revoked, or its variable is removed first, install and
rotation stop for the whole project with a failure that says so, and passing
`--gitlab-role-token` again does **not** recover: the project-wide exclusions
are resolved before any provided credential is processed. To recover:

1. In **Settings → CI/CD → Variables**, set the role's credential variable
   (`FULLSEND_GITLAB_POLLER_TOKEN`, `FULLSEND_GITLAB_ANALYST_TOKEN`,
   `FULLSEND_GITLAB_CODER_TOKEN`, or `FULLSEND_GITLAB_ROLE_<NAME>_TOKEN`) by
   hand to a working credential of the account that should own it.
2. Re-run `fullsend repos install`. Attribution authenticates with the
   installed credential and records its owner.
3. If the failed write was an ambiguous replacement, attribution proves
   ownership, not that publication completed, so automatic replacement stays
   held. Re-enroll with `--gitlab-role-token` or force a rotation with
   `--rotate-gitlab-roles`; either runs even though install reports the hold,
   and the normal lifecycle resumes afterwards.

<a id="poller-owned-trigger-token-deferred"></a>

#### Poller-owned trigger token

> **Poller elevation safety:** Re-elevating a Poller identity that ever held a
> distributed runtime credential requires a verified server-side guarantee
> that requests accepted before credential revocation, including asynchronous
> credential and job creation, have finished. The current GitLab adapter
> cannot establish that guarantee, so it never re-elevates the current Poller
> and creates new triggers through a fresh Poller generation instead. Polling
> continues with Developer credentials; compliant existing triggers can still
> be reused. Revocation and empty resource inventories alone do not prove that
> requests have drained.

GitLab binds a trigger token to the user who creates it and only Maintainers
can create one. Install therefore creates the token as a fresh
replacement Poller service account that has never held a distributed runtime
credential, rather than the installing Maintainer or the current Poller
([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)).
Neither of those owns the token, and the current Poller is never elevated.
Install records the replacement account in
`FULLSEND_GITLAB_POLLER_GENERATIONS`, creates an installer-only bootstrap
credential for it, grants it Maintainer only for the create call, restores
Developer and verifies it, and revokes the bootstrap credential. It then
records that the trigger may be distributed, stores the trigger secret, and
creates or updates the webhook. Only after both succeed does it publish the
replacement runtime credential and cut over. The previous Poller stays at
Developer as the retiring generation. A later install retires it once the
24-hour rotation grace after the cutover has elapsed and no unfinished job runs
as it ([#8244](https://github.com/fullsend-ai/fullsend/issues/8244)).
Retirement revokes the triggers that account owns, revokes its credentials,
deletes the account, confirms the deletion by listing again, and clears
`retiring_user_id`. It leaves any merge grant on the protected default branch
in place and reports it for operator review, because fullsend does not record
who created a per-user grant. A step that fails leaves the record for the next install to
retry, and never stops install from repairing the webhook. A trigger whose owner
GitLab omits is revoked only when it is a managed webhook trigger no webhook
uses; otherwise install names it so an operator can revoke it. Until retirement completes, a forced trigger rotation defers and keeps
the existing fast path, so at most one old/new Poller pair exists. A drifted
fast path that needs a replacement trigger (for example, a webhook whose URL no
longer carries the stored token) cannot be repaired until then. Install removes
it, because retirement cannot run while a managed webhook's trigger is
unidentifiable. It reports reconciliation as pending and names the blocker.
Once retirement completes, install recreates it. The current Poller
keeps polling throughout, so polling and in-flight jobs that authenticate as it
are not interrupted. If the trigger request, its verification, or the final
checks fail, install publishes nothing and quarantines the generation, which
blocks any new generation until an administrator reconciles it. If the trigger
secret store (including a write whose outcome is unknown) or the webhook
publication fails, the generation is not quarantined and no cutover is
recorded: the current Poller stays in service, and the next install revokes the
trigger, confirms by listing again that it is gone, never raises that account
again, records it as abandoned (it stays at Developer without credentials and
is deleted by uninstall), and finishes with a fresh account. If the cutover
itself is interrupted, the next install finishes it without minting another
trigger. Every install also reconciles the recorded current, retiring, and
abandoned Poller accounts: one that drifted above Developer is restored and
verified, and its credentials are contained when that cannot be verified. A
replacement account whose project role or branch policy cannot be established
before elevation is deferred, not raised; fullsend never writes a merge grant
for it. A Poller that is still a project access token bot leaves the fast path
deferred. See
[Poller identity generations](../contributing/gitlab-role-credentials.md#poller-identity-generations).

An absent `FULLSEND_GITLAB_POLLER_GENERATIONS` variable is an empty state. A
variable that exists but is blank fails closed, because recorded generation
state may have been lost. Re-runs reuse compliant existing credentials and
reconcile webhook configuration. Any project that is ineligible (no project
service accounts, an unsafe or unverifiable trigger policy, or a protected
default branch the replacement Poller cannot start pipelines on), or whose
generation state is ambiguous, defers the fast path and keeps polling. The
runtime role credentials stay Developer-level,
`ci_pipeline_variables_minimum_override_role` stays `no_one_allowed`, and
dispatch continues to use typed inputs.

#### Service-account status

`repos status` adds a `gitlab_service_accounts` JSON array with each account's
`id`, `name`, effective `access_level`, and whether ownership is `managed`,
without credential values. A managed account whose effective access differs
from Developer (30) adds `gitlab-service-account:<id>` drift and clears
`gitlab_roles_ready`. Missing recorded managed accounts also clear readiness.
Unreadable account inventories or ownership state produce diagnostics rather
than an apparently empty inventory.

Managed-account name drift or an inventory, ownership, or effective-access
verification failure adds service-account drift and marks role readiness
unverified. An unsupported service-account capability preserves legacy
readiness only when the rotation state records no managed accounts requiring
verification. Recorded managed accounts with an inaccessible inventory remain
unverified and add drift.

#### Service-account uninstall

For GitLab, uninstall removes Fullsend-owned webhook/trigger resources, their
wildcard-scoped secrets, poll-state branches and retired variables, role
credentials, and durably identified Fullsend-managed project service accounts.
It preserves contributions, administrator-supplied accounts and their tokens,
and accounts without ownership proof. Remaining credentials, SSH authentication
keys, unfinished jobs or owned schedules/triggers prevent account deletion.
Failed inventory, revocation or deletion retains the manifest entry and
ownership state for retry. Supplied-account exclusions survive successful
uninstall.

Matching account names alone never authorize reuse, membership changes or
credential cleanup. Install and rotation require durable managed ownership;
unverified same-named accounts are preserved rather than adopted.

Uninstall does **not** remove a leftover `FULLSEND_FORGE_TOKEN` secret or its
`fullsend-bot` project token; those require manual cleanup. It also retains the
`FULLSEND_GITLAB_POLLER_GENERATIONS` variable, which records quarantine and
retirement obligations for replacement Poller accounts and triggers, until an
administrator has reconciled them. Reinstall reconciles
managed legacy role identities to service accounts using ordinary rotation and
the existing in-flight-job grace period. It preserves administrator-supplied
identities and never introduces a fourth role identity.

#### Trigger-only service account

For GitLab repos, `repos install` also converges a separate trigger-only
project service account, under the project lease, after the Poller and its
generations are settled. It creates `fullsend-trigger-<nonce>`, temporarily
raises it to Maintainer so it can own a push-capable deploy key and a pipeline
trigger token, revokes its bootstrap token and returns it to Developer, grants
the key push on the exact-name protected default branch, and publishes the
trigger token to the masked, protected `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN`
variable. Scheduled polling uses the account's job token, not this variable.
While the dispatch activation is recorded, the managed webhook embeds this
token, and install retires the Poller-owned webhook trigger and
`FULLSEND_TRIGGER_TOKEN`
([#8509](https://github.com/fullsend-ai/fullsend/issues/8509)). The account
is never one of the Poller identities (managed Poller generations and owners
of supplied credentials are checked first). Account, deploy-key, and
trigger-token obligations are recorded in the `FULLSEND_GITLAB_TRIGGER_ACCOUNT`
ledger before creation, so a re-run finishes an interrupted install. Managed
pipeline schedules are recognized by their exact descriptions and verified
owner, rather than stored schedule IDs. In the settled state, the account owns
managed polling schedules only while dispatch is activated (see below). An
interrupted install can temporarily leave account-owned schedules after
activation is withdrawn; the next install hands them back. Any other schedule
the account owns makes install fail.

Install and rotation (`--rotate-gitlab-trigger-account`) defer (reported, not
an error; polling stays in place) when the default branch has only a wildcard
protected-branch rule, project service accounts are unavailable,
`ci_pipeline_variables_minimum_override_role` is not `no_one_allowed`, or the
default branch differs from the recorded one (run `repos uninstall`, then
install). A deferral for the override role that is weakened or cannot be
read also removes an already published token variable and revokes the
account's triggers; `repos status` reports the live role. Install fails, before the account is raised
to Maintainer, when the account owns an SSH key, a pipeline schedule, or a
trigger token that fullsend did not create. It also fails, naming the IDs and
deleting nothing, when a pipeline trigger token has no reported owner
(`owner: null`) or a pipeline schedule has no reported owner: whether either
acts as the trigger account is unknown, so ownerless schedules are reported as
unknown ownership and never attributed to the account. A schedule is the
account's only when GitLab reports its exact owner ID. A recorded deploy key
that cannot push is never reported ready, and the account's effective access
(including inherited group access) must be exactly Developer even when its
direct membership already says Developer.

Whenever install leaves the trigger account ready, including after GitLab
refuses the Poller protected-ref grant, it also rechecks the dispatch
activation gate (`CheckGitLabTriggerDispatchGate`) and reports either
`GitLab trigger dispatch activation deferred: <reasons>` or
`GitLab trigger dispatch prerequisites verified`. The recheck lists every
unmet prerequisite: managed scheduled polling no longer uses
pipeline variables (the canonical schedule specs and every live managed
schedule; a failed schedule read defers), the live
`ci_pipeline_variables_minimum_override_role` is exactly `no_one_allowed`
(weaker, unreadable, and unsupported all defer, even when a trigger token is
already published), CI job-token repository push
(`ci_push_repository_for_job_token_allowed`) is disabled (enabled, unreadable,
and unreported all defer), and the trigger account is ready on the default branch,
verified against live GitLab state. A legacy install, whose schedules still set
`FULLSEND_POLL_MODE`, defers, and so does a committed CI job that a trigger,
agent child (`source=pipeline`) or scheduled (`source=schedule`) pipeline would
run besides Fullsend's own. Activation also defers until the committed
dispatch scripts and their pre-authorization helpers match the shipped
compatible scripts. The managed poll, agent, and dispatcher jobs must keep
their shipped job configuration, including image, cache, automatic admission,
and scripts. The committed root and wrapper must admit scheduled polls and
include the `poll` stage. The committed files that
consume an agent child (`source=pipeline`) pipeline (the root workflow, the root
and wrapper includes, the agent job rules and input bridges, the shipped input
schemas that constrain values before job-name expansion, and `run-agent-job.sh`) must admit
it. Customized or older versions need a merged scaffold repair; a selector name in a comment or unexecuted code does not establish
readiness. Activation also defers when the project selects a custom CI
configuration path, when a project, group, or instance CI/CD variable
overrides a dispatch or pre-authorization value such as `CI_PIPELINE_SOURCE`,
`CI_API_V4_URL`, `CI_PROJECT_DIR`, `CI_JOB_TOKEN`,
`CI_PIPELINE_SCHEDULE_DESCRIPTION`, `FULLSEND_POLL_MODE`, or an agent input
bridge, or when either
inventory cannot be read (group and instance scopes the installing credential
may not read are skipped). GitLab also requires a protected-branch schedule
owner to have merge permission. If the Developer trigger account lacks it,
the schedules stay installer-owned on the Poller route. GitLab Free cannot
grant merge access to just that account; a project whose protected branch
allows only Maintainers to merge therefore uses the fallback without widening
merge access. An exact-name managed schedule left by a previous installer
credential remains eligible for adoption or hand-back when its reported owner
still has project Maintainer access. An unknown or lower-privilege owner
requires manual reconciliation.
Every converge, rotate, containment or uninstall run first deletes the
`FULLSEND_GITLAB_DISPATCH_ACTIVATION` variable, before it rechecks the gate.
When the gate passes, `repos install` records that variable, bound to the exact
`FULLSEND_GITLAB_TRIGGER_ACCOUNT` ledger generation, which activates the
CI_JOB_TOKEN child-pipeline (`source=pipeline`) dispatch route; install fails
if recording the activation fails. Install then recreates the managed polling
schedules (`fullsend slash poll`, `fullsend event poll`) under the trigger
account, so scheduled polls run as the account and use that route. After this
ownership move, use this release or newer for `repos install`,
`repos status`, and `repos uninstall`. Once this release writes the
`trigger_token_digest` or `publication_pending` ledger key, older strict CLI
decoders reject the ledger even if schedules remain installer-owned. Older
CLIs also treat account-owned schedules as foreign and refuse cleanup. Upgrade the CLI before retrying; do not delete those
schedules by hand. The account creates them with a short-lived token while it
stays at Developer. Each uses the managed spec's UTC cron and recorded branch,
and keeps the enabled or disabled state of the schedule it replaces,
and the installer-owned originals are deleted only after the replacements are
confirmed. A spec with no schedule at all is left to schedule convergence and
moved on the next install
([#8508](https://github.com/fullsend-ai/fullsend/issues/8508)).
An account-owned managed schedule that carries pipeline variables is handed
back to the installer after the containment-only recovery, the repository
selection check and the configuration preflights, before convergence reaches typed activation, which only reads managed schedules, refuses
while one still sets a pipeline variable, and cannot edit a schedule the account owns. The repair inspects the current and every retired
generation whether or not the current one is ready or published, and the
ownership stage after the install adopts the schedules again once the
activation is recorded. A failure of that early repair is reported and the
install continues.
With `--reactivate-schedules`, the ownership stage reactivates a disabled
account-owned schedule only when the committed-template prerequisites schedule
convergence requires also hold (the typed wrapper, the root pipeline-input
contract, the agent and poll templates, and the verified override restriction);
otherwise it reports `Not reactivating disabled GitLab polling schedules` and
keeps the schedule disabled.
A deferral leaves the activation cleared and hands any account-owned managed
schedule back to the installer, so polling stays the dispatch path through the
Poller credential. When rotation or repair records the activation for the
replacement generation, the ownership stage moves the managed schedules the
retired account still owns straight to the replacement account in the same run,
before the retirement that follows the webhook cutover
([#8602](https://github.com/fullsend-ai/fullsend/issues/8602)). Otherwise
retirement hands a retired account's schedules back to the installer before
deleting it, and uninstall deletes them. The ledger carries no
activation field. This version adds the `trigger_token_digest` and
`publication_pending` fields, so older strict CLI decoders reject a ledger
written by this version before they read its version; see the
[older-CLI behavior and rollback](../contributing/gitlab-role-credentials.md#trigger-only-service-account)
note.

As soon as the manifest and forge clients are available, before the
configuration preflights or any convergence, `repos install` runs a
containment-only recovery for every targeted GitLab repository that has a
ledger. Under the
project lease it revokes recorded bootstrap tokens and restores Developer
access for the current and any retired generation, and it creates nothing. It
also removes an already published token variable and revokes every owned
trigger, across all generations, when the live override role is weakened or
cannot be read or the variable is unsafe. Each removal and revocation is
attempted independently and failures are reported together. It
does not read the Poller inventory, so a malformed or unreadable inventory, or
a failure in the convergence, scaffold, Poller, role, protected-ref, override
or webhook stages, cannot leave the account elevated or a published credential
usable while overrides are allowed. It leaves the webhook
fast path untouched. A repository whose containment failed is reported as
failed and skips the later stages. `--dry-run` only reports that it would run.

A published trigger credential is never re-elevated. Repair and
`--rotate-gitlab-trigger-account` create a fresh generation (new nonce,
account, deploy key, and trigger token), publish it, verify that the managed
webhook no longer carries the retired token, and only then retire the
previous one (revoke its trigger and push entry, delete its deploy key and
account). Only a managed webhook that still names or embeds a retired
generation's credential blocks the retirement, so it proceeds when the webhook
fast path is deferred and no managed webhook exists. The previous account stays at Developer until retirement, which is
recorded in the ledger and retried by install and uninstall. Install and status
also verify the `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN` variable is present,
masked, protected, not file-typed, and not environment-scoped; install repairs
drift by removing the variable fullsend owns and revoking its
ownership-verified trigger, then publishing a replacement from a fresh
generation. Variable deletion and trigger revocation are attempted
independently, so a failed deletion still revokes the owned trigger. Either
failure stops the replacement, the errors are reported together, and the
unresolved obligations stay in the ledger. The secret never falls back to an
unmasked variable: if GitLab refuses to mask the value, create and update both
fail.

`repos status` re-diagnoses the account against GitLab and adjusts the
drifted-repository count in both directions: live drift is counted, and
ledger-only drift that GitLab disproves is not. If the live repository read
fails, a ledger-only `ready` is reported as `unreadable` drift instead.

`repos status` adds a `gitlab_trigger_account` object with a `state`
(`not-installed`, `ready`, `drifted`, `deferred`, `in-progress`,
`cleanup-pending`, or `unreadable`) and exact `reasons`, including token
variable drift and an unfinished retirement of a previous generation. While
the dispatch activation is recorded, a managed polling schedule that the
trigger account does not own (it runs with its owner's permissions) is
reported as drift until `repos install` moves it. When its owner is not a
retired generation or a verified installer Maintainer, install refuses to adopt
it, and an administrator must remove or reassign it first. States
other than `not-installed`, `deferred`, and `ready` add `gitlab-trigger-account`
drift.

`repos status` also adds a `gitlab_webhook` object for the webhook fast path
([ADR 0125](../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)). It has a
`state`, the booleans `webhook_present`, `webhook_configured`,
`trigger_token_ready` and `webhook_secret_ready`, and exact `reasons`. Like the
rest of status output, it names variables and IDs only and never shows the
trigger token, the webhook secret or the webhook URL. The states are:

- `ready`: one managed webhook carries the active trigger credential with the
  desired configuration.
- `missing`: every gate holds but no managed webhook exists yet.
- `drifted`: something is misconfigured or an unmet contract blocks the fast
  path. Misconfiguration includes the webhook's trigger ref, events or SSL
  verification, a disabled or duplicate webhook, a missing or unsafe
  `FULLSEND_TRIGGER_TOKEN` or `FULLSEND_WEBHOOK_SECRET`, superseded trigger
  tokens, or leftover Poller-owned trigger credentials on the trigger-account
  route. Unmet contracts include the trigger-token safety invariants (such as
  `ci_pipeline_variables_minimum_override_role` not being `no_one_allowed`)
  and readiness gates (such as a wrapper or root `.gitlab-ci.yml` without the
  typed pipeline-input contract). Unlike the trigger account, the webhook
  status does evaluate these project-wide safety invariants.
- `deferred`: nothing is provisioned (no managed webhook, trigger token or
  published trigger-account credential) and the pipeline wrapper on the default
  branch does not include the dispatcher yet. A published trigger-account
  credential, including one from an interrupted rotation or an unreadable
  ledger, counts as provisioned, so the safety invariants are still evaluated.
  This covers a fresh install
  whose initialization MR has not merged and a scaffold version that predates
  the fast path.
- `unreadable`: the fast path could not be verified.

States other than `ready` and `deferred` add `gitlab-webhook` drift. The table
output lists the reasons for every state except `ready`. `repos install`
creates a missing webhook and repairs a misconfigured one. When the repair
needs a replacement trigger token and the Poller generation state blocks it
(for example, a superseded Poller generation has not finished retiring), the
reasons include `reconciliation pending:` followed by the blocker.

`repos uninstall` removes the published token, trigger token, push entry,
deploy key, the managed polling schedules the account owns (deleted without an
installer-owned copy), and the account under the project lease, after the webhook fast path
and before role-identity cleanup, and then deletes the ledger (generic
role-variable cleanup never deletes the ledger or the token variable). A failure, or an
unverified Poller inventory, stops uninstall, keeps the ledger and the manifest
entry, and a retried uninstall resumes. Resources that no longer match what was
recorded are left in place for an administrator to inspect.

#### Upgrade compatibility

`FULLSEND_GITLAB_INSTALL_LEASE` serializes project lifecycle operations and is
removed when the operation finishes. Older CLIs do not honor it and can report
it as orphan drift; do not run them concurrently. If an installer dies, stop
all project installers, restore and verify effective Developer access for the
Poller, then remove the lease in **Settings → CI/CD → Variables** and rerun
install. Never delete a live installer's lease.

> **Operators must upgrade.** Every write to `FULLSEND_GITLAB_ROLE_ROTATION`
> now carries `"version": 2`. Upgrade every CLI that runs `repos install` or
> converge against a project before running this version against it.
> Dispatch is unaffected: the polling path never reads rotation state.

The current CLI reads and writes only version 2. Version 1 is never written.
CLIs built with the tolerant reader ([#8233](https://github.com/fullsend-ai/fullsend/issues/8233))
accept version 1 but ignore the ownership fields (`created_token_ids`,
`supplied_token_id`, `supplied`, `excluded_user_ids`) when they select outgoing
tokens or run grace cleanup, so a version 1 document could let them revoke an
administrator-supplied credential. They reject version 2 before touching any
credential, and older CLIs reject the unknown `version` key, so a
mixed-version rotation or status run, which reads rotation state before it
mutates anything, is rejected before it touches a credential. That guarantee
does not cover every older code path: older provisioning can publish missing
supplied or minted secrets before it records distribution state, and older
uninstall cleanup never reads rotation state, so it deletes identity variables
and revokes active role-named project tokens, supplied ones included. Never run
an older CLI for install or uninstall against a project this version manages.
No CLI refuses state silently and then rewrites it: an unsupported version is
never rewritten.

> **BREAKING CHANGE ([#8246](https://github.com/fullsend-ai/fullsend/issues/8246)):**
> the current CLI no longer reads the legacy unversioned format or version 1,
> and rejects documents with unknown fields. It refuses such state before
> touching any credential and does not rewrite it. A project whose state was
> never converged by a CLI that writes version 2 must first be converged once
> by a CLI that includes
> [#8313](https://github.com/fullsend-ai/fullsend/pull/8313) but predates the
> #8246 change, which rewrites the state as version 2. Do not edit the
> `version` field by hand.

The upgrade is one-way. After the first install, converge or uninstall with a
CLI that includes this change ([#8313](https://github.com/fullsend-ai/fullsend/pull/8313)),
the project's state is version 2 and the rewrite is not reverted by running an
older CLI.

**Minimum compatible CLI.** Any build that reads rotation state version 2 is
compatible: the first release whose notes list
[#8313](https://github.com/fullsend-ai/fullsend/pull/8313), or a source build
whose checkout contains the commit that merged it. This page does not name a
release number because none has shipped with the change yet. The
unsupported-version error reports the state version a CLI must read, and names
this section.

> **Release gate (pending maintainer verification).** The operator-upgrade
> notice above and the release announcement for this one-way migration have
> not yet been verified by maintainers. The release that first ships #8313 must
> not be announced, and this requirement must not be treated as communicated,
> until a maintainer has confirmed both. This note is removed once that
> verification is recorded.

Downgrading is not supported, so upgrade the CLI instead. If a rollback is
unavoidable, preserve the ownership exclusions:

1. Stop all project installers and every CLI that targets the project.
2. Copy the current value of `FULLSEND_GITLAB_ROLE_ROTATION` from **Settings →
   CI/CD → Variables** to a safe place, and note every administrator-supplied
   service account and token on the project (the `supplied`, `supplied_user_id`,
   `supplied_token_id` and `excluded_user_ids` entries).
3. Do not run an older CLI for install, converge or uninstall against the
   project, and do not edit the `version` field down: a CLI that accepts the
   edited document ignores the ownership fields and can revoke a supplied
   credential.
4. If any exclusion is recorded, keep the variable in place and recover forward
   by running a CLI that reads version 2. Deleting the variable discards the
   exclusion records.
5. Only when no supplied account or exclusion is recorded, follow the
   uninstall-then-reinstall procedure below. Remove the variable by hand only
   as a last resort for damaged state, and never while any installer is
   running.
6. If the variable was lost or deleted with exclusions recorded, re-enroll each
   noted supplied credential with `--gitlab-role-token` using a CLI that reads
   version 2 before running any other lifecycle operation.

Use the upgraded CLI consistently for install, rotation, status and uninstall
after service-account convergence. Mixed-version lifecycle operations and
downgrading the CLI after convergence are unsupported: older versions cannot
interpret the managed-account ownership and supplied-account exclusions.
Downgrading is unavailable while any administrator-supplied account exclusion
remains. Successful uninstall preserves that exclusion in the rotation document,
and the older CLI rejects the document because it does not recognize the
version or exclusion fields. Do not delete the ownership protections to work
around this. Where no supplied-account exclusions exist, stop lifecycle
operations, uninstall using the upgraded CLI, confirm managed cleanup
succeeded, then reinstall with the older version. Administrator-supplied
accounts remain untouched.

Upgrade notes: Convergence replaces managed
project-access-token bot identities with project service-account identities;
consumers must not rely on the previous bot user ID. Status JSON adds
`gitlab_service_accounts`; managed accounts not exactly Developer produce
drift. Successful uninstall preserves supplied account exclusions, and
incomplete managed-account cleanup retains ownership. With the live handoff
([#8243](https://github.com/fullsend-ai/fullsend/issues/8243)),
trigger creation uses a fresh replacement Poller service account that is
temporarily raised to Maintainer with an installer-only bootstrap credential,
creates the trigger, and is verified back at Developer before any cutover; the
current Poller is never elevated or modified and keeps polling throughout.
Polling provides catch-up until the replacement runtime credential is
published.

### Common workflows

Converge all repos from a manifest (provision new, repair component drift, repair scaffold content drift, refresh a declared configuration preset, rewrite a drifted managed configuration file, upgrade refs):

```bash
fullsend repos install -f repos.yaml
```

Preview changes without modifying infrastructure:

```bash
fullsend repos install -f repos.yaml --dry-run
```

Declare a new repo, then install it:

```bash
fullsend repos set 'github.repos["acme/new-repo"]' '{"inference": {"auth": "vertex-wif"}}'
fullsend repos install acme/new-repo --direct --vertex-project "<gcp-project>"
```

Switch one existing repo to OpenAI API key authentication:

```bash
fullsend repos set 'github.repos["acme/api"].inference.auth' openai-api-key
fullsend repos install acme/api --openai-api-key "$OPENAI_API_KEY"
```

Change a persistent setting, such as the agent roles or a Vertex region, for every repo:

```bash
fullsend repos set defaults.config.roles '[triage, coder, review]'
fullsend repos set defaults.config.inference.region us-east5
fullsend repos install --dry-run
fullsend repos install
```

Install specific repos:

```bash
fullsend repos install acme/api acme/web
```

Declare a GitLab repo and install it:

```bash
fullsend repos set gitlab.url https://gitlab.example.com
fullsend repos set 'gitlab.repos["group/project"]' '{"inference": {"auth": "vertex-wif"}}'
fullsend repos install group/project --direct --vertex-project "<gcp-project>"
```

In `fullsend repos status --json`, `gitlab_roles_ready` is true only when the
base role diagnosis, built-in role readiness, and every registered-role mapping
are ready. GitLab status always evaluates role credentials; missing role
secrets are unconditionally reported as drift.

## `repos status`

Read-only comparison of the `repos.yaml` manifest against actual forge state. Reports installation status and configuration drift for each repo, including declared configuration-preset drift against `.fullsend/config.base.yaml` and managed configuration drift against `.fullsend/config.yaml`. An existing `.fullsend/config.base.yaml` with no resolved preset is reported as drift. Both configuration files are checked even for a repo with no Fullsend components yet: an undeclared base, a differing preset base, or a managed `.fullsend/config.yaml` that lacks the ownership marker is reported before first install. Files that install would simply add are not reported as missing until the repo is installed. An existing zero-byte `.fullsend/config.yaml` counts as an existing markerless file, so it also requires adoption. For every markerless overlay, the table and the JSON `detail` field show the proposed `repos.yaml` entry that carries each existing setting (all under the entry's `config`, including `runtime`, `allowed_remote_resources`, and values equal to the code defaults) the setting-level difference between the existing file and the managed file adoption would produce, and the effective layered-configuration change (the existing overlay over the installed base compared with the managed overlay over the base layer the run would leave in place), so the operator can see which settings would be lost, or would change only through the base layer, before adopting. A markerless overlay that is malformed or not a per-repo configuration is reported as a status error rather than adoption guidance.

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
- **DRIFT** — Fields that differ from the manifest, scaffold files whose template content has changed, orphan files or variables no longer in the managed set, stale assets from an earlier vendored install (`vendored-assets`, see [Turning vendoring off](#turning-vendoring-off)), or `none`

Inference credentials are checked against each repo's own effective `inference.auth`, so a mixed fleet is evaluated repo by repo (see [Inference credentials](#inference-credentials)):

- A missing secret or CI/CD variable of the selected method is reported as drift on that name (for example `FULLSEND_OPENAI_API_KEY` reported as `missing`). The other method's credentials are not required.
- An `openai-wif` repo reports drift on `identifiers` when no complete OpenAI WIF identifier set is live on the default branch (missing, or partially configured). Only identifier names are reported. The GCP secrets are optional and `FULLSEND_OPENAI_API_KEY` is reported as obsolete. Drift on `workflows` means an installed caller or its actual reusable-workflow target cannot forward WIF identifiers, obtain an OIDC token, or run with the available secrets. Status checks live targets, including vendored workflows, rather than assuming the manifest pin has landed.
- An `openai-api-key` repo on GitHub reports drift on `residual-identifiers` when OpenAI WIF identifiers (complete or partial) stay active in an organization variable it inherits or in `inference.openai` configuration on the default branch, since the runtime prefers them over the API key. Repository-scoped identifier variables are reported as orphans. Only names are reported. `repos install` rejects such a repo until the residual variables or `inference.openai` blocks are removed.
- A Fullsend-managed secret of the method that is **not** selected (`FULLSEND_GCP_PROJECT_ID` / `FULLSEND_GCP_WIF_PROVIDER` on an `openai-api-key` repo, or `FULLSEND_OPENAI_API_KEY` on a `vertex-wif` or `openai-wif` repo) is reported as obsolete drift (`expected absent`). So is a leftover `FULLSEND_GCP_REGION` variable on an `openai-api-key` repo, and a `FULLSEND_GCP_SERVICE_ACCOUNT` variable on a GitLab repo that does not declare `inference.service_account` or does not use `vertex-wif`. This drift is reported even when the repo is otherwise not installed. `repos install` removes these once the replacement configuration is on the default branch. Only presence is checked, so values are never read or printed.
- `FULLSEND_MINT_URL`, `FULLSEND_GCP_REGION`, and `FULLSEND_GCP_SERVICE_ACCOUNT` are compared with the same [effective infrastructure values](#effective-infrastructure-values) install would write, so status, `--dry-run`, and install agree. `FULLSEND_GCP_SERVICE_ACCOUNT` is compared only when `inference.service_account` is declared; a missing or different variable is then drift. A declared `inference.wif_audience` is rendered into the expected `.gitlab/ci/fullsend-agent.yml`, so an agent job still requesting `aud: "fullsend"` is reported as scaffold drift. `FULLSEND_GCP_REGION` is compared only when a region is declared, and then a missing variable is reported as drift just like a changed value; an undeclared region is never reported as drift. A mint URL conflict between the manifest and the effective configuration is reported as a configuration error for that repo, as is a declared Vertex configuration install would reject (an invalid `inference.project` or `inference.wif_provider`, or a `wif_provider` without a declared `project`; install rejects the latter even when `--vertex-project` is passed).
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
capability contract, and job-to-identity mapping). These checks run in two
stages. The initial status check reads only the role credential state and
does not list the project access token inventory, so it cannot see credential
lifecycle. A later lifecycle check then lists the project access token
inventory for each GitLab repo. When the inventory is listed, expired,
revoked, or unverified role credentials downgrade readiness. An enrolled
administrator-supplied credential is also authenticated, and its owner's
effective Developer access is rechecked, whether or not the inventory lists
it; a revoked or expired one is reported as unhealthy rather than checked for
owner access. The lifecycle check fails closed: when token inventory cannot be
listed, status clears role readiness and reports `gitlab-token-inventory`
drift instead of leaving roles ready on an unverified inventory. A present shared
`FULLSEND_FORGE_TOKEN` is not treated as a substitute for a missing
built-in role. Registered roles with no agent mapping are also reported as
not ready. These checks do not retire the shared
token. Status also reports `protected-ref-pipeline` drift when the poller
cannot create pipelines on the protected default branch. Every active Poller
identity needs pipeline access on that branch: the project service account,
the project access token user (`fullsend-poller`), and the owner of an
administrator-supplied Poller credential. When Developer merge/push is absent,
each of those identities must be listed individually in `allowed_to_merge` /
`allowed_to_push`. A grant for one identity does not cover another, so drift
is reported when any active Poller identity is missing.
For the service-account diagnostics, see
[GitLab project service-account lifecycle § Service-account status](#service-account-status).

**JSON output** (`--json`) returns the full `StatusResult` object with per-repo details and aggregate summary counts.

### Exit codes

The command returns a non-zero exit code when any repo has drift, is not installed, or encountered an error. This makes it suitable for CI checks.

### Authentication

Requires a GitHub token via `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`. For GitLab repos, set the `GITLAB_TOKEN` environment variable or pass `--gitlab-token` to the `repos` command group.

## `repos uninstall`

Tear down fullsend from the specified repos and remove them from the manifest. By default, the command tears down first (opening a PR to remove workflow files, then deleting variables and secrets via the API), then removes successfully-torn-down repos from the manifest. Partial failures leave the manifest entry intact so the user can retry.

File deletions (workflow YAML, `.fullsend/config.yaml`, and GitLab `.gitlab-ci.yml` unmerge) are delivered as a pull request unless `--direct` is set, matching `repos install`. Variable and secret deletions are API-only operations and always happen immediately. For GitLab repos, uninstall also deletes the `fullsend-poll-state-slash` and `fullsend-poll-state-events` branches (a missing branch is ignored so older installs still uninstall cleanly) after deleting the Fullsend-owned webhook fast-path project webhook (hooks Fullsend does not own are left untouched) and revoking its managed Poller-owned pipeline trigger token before any scaffold file is removed, and deleting the wildcard-scoped `FULLSEND_TRIGGER_TOKEN` and `FULLSEND_WEBHOOK_SECRET` variables (a failure there fails uninstall and leaves the manifest entry in place for retry). On the trigger-account route, `FULLSEND_TRIGGER_TOKEN` was already deleted during install and the webhook's trigger is revoked during the trigger-account removal described below. Uninstall also deletes the retired poll-state CI/CD variables, registry/rotation-state variables (`FULLSEND_GITLAB_POLLER_GENERATIONS` is deleted last, only after every Poller account it records was verified and deleted; uninstall retains it when cleanup is incomplete or ambiguous, so the quarantine and retirement obligations it records survive until an administrator has reconciled them), built-in and custom `FULLSEND_GITLAB_*_TOKEN` secrets, and the corresponding `fullsend-poller` / `fullsend-analyst` / `fullsend-coder` / `fullsend-role-*` project access tokens. Uninstall does **not** delete a leftover `FULLSEND_FORGE_TOKEN` secret or revoke a matching `fullsend-bot` project access token — a repository installed before the role-only rollout requires manual cleanup of those. Token revocation is part of uninstall success: if listing or revoking the role project access tokens fails, uninstall fails and the manifest entry is left in place for retry. Reinstall and converge do not revoke credentials that are already distributed. Before deleting anything, uninstall reads the rotation-state variable and fails closed, leaving every variable, secret, and token in place, if it is unreadable or malformed. The rotation-state variable is deleted last, after every other cleanup step succeeds; when it records administrator-supplied service-account exclusions, an exclusions-only document is retained so a later install or uninstall does not treat those accounts as Fullsend-managed. When the rotation state records an administrator-supplied credential whose owner was never recorded and the CLI's token client cannot attribute it, uninstall does not guess: it leaves the supplied role's project access token and the whole rotation-state variable in place (reporting this in the cleanup diagnostics) and still removes everything else.

GitLab uninstall holds the `FULLSEND_GITLAB_INSTALL_LEASE` project lease for the whole teardown, from webhook removal through the final variable, secret, branch, and token deletions, so no concurrent installer can publish a replacement hook, trigger token, or role credential mid-teardown. It fails closed: if another installer holds the lease or the client cannot take it, uninstall stops before removing anything and the manifest entry is left in place for retry. See [Upgrade compatibility](#upgrade-compatibility) for stranded-lease recovery.

The GitLab file removal, whether by pull request or `--direct`, also deletes
the webhook dispatcher scaffold (`.gitlab/ci/fullsend-dispatcher.yml` and
`.gitlab/ci/scripts/run-dispatcher-job.sh`). The `.gitlab-ci.yml` unmerge
removes Fullsend's `trigger` and child-pipeline workflow rules along with its
include, stages and other rules, and keeps the user's own entries. A repository
whose fast path was never provisioned (installed before it shipped, or never
converged) uninstalls cleanly: an absent webhook, trigger token, credential
variable or dispatcher file is skipped. Uninstall leaves the project's
`ci_pipeline_variables_minimum_override_role` setting unchanged. The restrictive
`no_one_allowed` value is safe to keep; relax it yourself in the GitLab project
settings if other pipelines need pipeline-variable overrides.

For the service-account cleanup, see
[GitLab project service-account lifecycle § Service-account uninstall](#service-account-uninstall).

For GitLab, uninstall first contains an interrupted trigger-account bootstrap under the project lease, before the Poller is reconciled and without reading the Poller inventory: it revokes recorded bootstrap tokens and restores Developer access, and a containment failure stops the uninstall. Uninstall then also removes the trigger-only service account after the webhook fast path and before role-identity cleanup: the published `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN` variable, the trigger token, the protected-branch push entry, the deploy key, the managed polling schedules the account owns (deleted without an installer-owned copy), the account, and finally the `FULLSEND_GITLAB_TRIGGER_ACCOUNT` ledger. Before deleting the account, uninstall and rotation retirement check that it owns no active personal access token, SSH key, pipeline schedule or trigger token that Fullsend did not create, and that each of those inventories can be read; otherwise the account is kept. The ledger is kept until cleanup succeeds, so a retried uninstall resumes. See [Trigger-only service account](#trigger-only-service-account).

Uninstall PR delivery intentionally reuses the same branch as `repos install`/`converge` (`fullsend/scaffold-install`), since already-deployed per-repo shims only exclude that branch name from dispatch. **Known limitation:** if an install PR is still open on that branch when uninstall runs (or an uninstall PR is open when install/converge runs), the existing PR is updated with the new commit but its title and body are left unchanged — the PR may show an install-oriented title while its diff now removes files, or vice versa. Check the PR's diff, not just its title, before merging when install and uninstall run close together against the same repo.

GCP WIF pool/provider cleanup for GitHub repos is handled separately via `inference deprovision`. This does not cover GitLab's shared `gitlab-oidc` WIF provider — for GitLab repos, see [Operations § Per-repo teardown](../guides/getting-started/operations.md#per-repo-teardown) step 6 to revoke that repo's WIF trust.

When multiple repos are targeted (via globs or explicit bulk lists), the command prompts for confirmation unless `--yes` is set. Credentials are required only for the forges of the targeted repos. Uninstall does not require an `inference.auth` selection, so manifests without one can still be torn down.

Uninstall removes every Fullsend-managed inference credential, whatever the repo's `inference.auth` is (or whether it is set or valid at all): `FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER`, and `FULLSEND_OPENAI_API_KEY`, plus the `FULLSEND_GCP_REGION` variable (and, on GitLab, `FULLSEND_GCP_SERVICE_ACCOUNT`). Leftovers from an earlier method and partially installed repos are cleaned up the same way. A credential that is already absent is skipped, so re-running uninstall is safe. Uninstall never deletes an unprefixed `OPENAI_API_KEY` secret or CI/CD variable, or any other credential Fullsend does not manage. It also leaves the user-managed `FULLSEND_OPENAI_AUDIENCE`, `FULLSEND_OPENAI_IDENTITY_PROVIDER_ID` and `FULLSEND_OPENAI_SERVICE_ACCOUNT_ID` identifier variables in place. Remove `OPENAI_API_KEY` yourself if nothing else uses it.

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

## `repos set`

Set or remove any writable field in `repos.yaml`: `defaults`, the `github` and `gitlab` sections, and individual repository entries (concrete or glob), including nested `config` fields and structured values such as lists, objects, and whole entries.

```bash
fullsend repos set <path> <value>
fullsend repos set --unset <path>
```

Paths are resolved against the manifest schema, so every field the manifest accepts can be set and there is no separate list of keys. After each edit the whole resulting manifest is validated with the same checks `repos install` uses. A rejected edit leaves the file byte-identical. A successful edit changes only the edited lines and keeps unrelated values, comments, blank lines, and ordering. If the manifest does not exist, `repos set` creates it with `version: 1`. Missing optional parent mappings (for example `defaults.config`) are created as needed.

A manifest loaded from an HTTPS URL, or a local file without write permission, is read-only: `repos set` reports `manifest <path> is read-only; apply the equivalent edit to its source, then re-run` and writes nothing.

### Path syntax

| Form | Example | Meaning |
|------|---------|---------|
| Dot-separated field names | `defaults.config.runtime`, `github.mint_url`, `gitlab.agent_runner_tags` | A field at `defaults`, forge-section, or nested scope |
| Repository selector | `github.repos["acme/api"]` | The repository entry named exactly `acme/api` |
| Field under an entry | `github.repos["acme/api"].config.runtime` | A field of an existing repository entry |
| Glob entry | `github.repos["acme/*"].inference.auth` | The glob entry whose `name` is literally `acme/*` |
| Map key selector | `defaults.config.models.aliases["opus"]` | A key of a map-valued field (a plain `.opus` also works) |

The selector is a double-quoted string with Go/JSON escapes, so entry names may contain dots, `*`, `?`, brackets, and other path characters without ambiguity. Selectors match entry names exactly, including case; a case-only mismatch is reported with the existing spelling. Quote the whole path in your shell, for example `'github.repos["acme/api"].config.runtime'`.

The path identifies the forge, so there is no `--forge` flag. Platform-specific fields are checked against the forge: `gitlab.mint_url`, `gitlab.app_set`, and `github.agent_runner_tags` are rejected with an error that names the field.

### Repository entries

- **Nested paths require the entry to exist.** `github.repos["acme/api"].config.runtime` fails without writing if there is no entry named exactly `acme/api`, so a typo cannot enroll a repository.
- **Whole-entry paths create or replace.** Setting `github.repos["acme/api"]` to an object creates the entry (appending it to the list) or replaces an existing entry in place. The selector supplies `name`; a `name` in the value must match it.
- **Whole lists.** `github.repos` and `gitlab.repos` can be set to a list of entries or removed with `--unset`.
- Duplicate entries (including the same repository in both forge sections), a mismatched name, and removing `name` are rejected.

### Values

Values are parsed according to the type of the target field:

| Field type | Accepted value | Notes |
|------------|----------------|-------|
| String | Any text, taken literally | `none` and `""` are stored as strings. `none` keeps its meaning as the explicit inheritance stop where the field supports it. |
| Boolean | `true` or `false` | `false` is stored explicitly and is distinct from an absent key |
| Number | A number | |
| List | YAML or JSON list, e.g. `'[gpu, linux]'` or `'["gpu"]'` | `[]` stores an explicit empty list |
| Object | YAML or JSON mapping, e.g. `'{"runtime": "claude"}'` | `{}` stores an explicit empty mapping. Unknown keys, YAML aliases, and YAML merge keys (`<<`) are rejected. |

An empty argument never deletes a key. Use `--unset <path>` to remove a key; removing a key that is already absent is not an error and leaves the file unchanged. Removing the last key of a mapping leaves an explicit empty mapping (for example `config: {}`).

Unknown, misspelled, or wrong-scope paths fail with an error that names the path, lists the valid fields at that scope, and points to the scope where the field is valid when there is one (for example `defaults.mint_url` points to `github.mint_url`). `defaults.runtime`, `defaults.allowed_remote_resources`, and the repository-entry equivalents point to the matching `config` path. `version` cannot be changed, and `gitlab.runner_tags` (the deprecated alias of `gitlab.agent_runner_tags`) can only be removed.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f`, `--manifest` | `repos.yaml` | Path to repos.yaml |
| `--unset` | `false` | Remove the key at `<path>` instead of setting it |

### Examples

Select Vertex WIF inference authentication for every repo, and OpenAI API key authentication for GitLab repos:

```bash
fullsend repos set defaults.inference.auth vertex-wif
fullsend repos set gitlab.inference.auth openai-api-key
```

Set a per-repo config field for every repo, for one forge, or for one repo:

```bash
fullsend repos set defaults.config.runtime claude
fullsend repos set github.config.allowed_remote_resources '["https://github.com/acme/"]'
fullsend repos set 'github.repos["acme/api"].config.runtime' pi
```

Add a repository entry, or replace an existing one:

```bash
fullsend repos set 'github.repos["acme/api"]' '{"inference": {"auth": "vertex-wif"}}'
fullsend repos set 'gitlab.repos["group/sub/project"]' '{}'
```

Set GitLab runner tags. `gitlab.control_runner_tags` is independent of `gitlab.agent_runner_tags`; left unset, control-plane jobs render `tags: []` (untagged):

```bash
fullsend repos set gitlab.agent_runner_tags '[fullsend-agent, gpu-runner]'
fullsend repos set gitlab.control_runner_tags '[fullsend-api]'
```

Set the GitLab instance URL and pin a ref:

```bash
fullsend repos set gitlab.url https://gitlab.example.com
fullsend repos set github.fullsend_ref v2.5.0
```

Delete keys:

```bash
fullsend repos set --unset github.mint_url
fullsend repos set --unset gitlab.agent_runner_tags
fullsend repos set --unset 'github.repos["acme/old"]'
```

### Migrating from `repos set-default` (breaking)

`repos set-default` has been removed and has no alias. Use `repos set` with the same key as the path:

| Before | After |
|--------|-------|
| `fullsend repos set-default github.fullsend_ref v2.5.0` | `fullsend repos set github.fullsend_ref v2.5.0` |
| `fullsend repos set-default github.mint_url ""` | `fullsend repos set --unset github.mint_url` |
| `fullsend repos set-default gitlab.agent_runner_tags "a,b"` | `fullsend repos set gitlab.agent_runner_tags '[a, b]'` |
| `fullsend repos set-default gitlab.runner_tags a` | `fullsend repos set gitlab.agent_runner_tags '[a]'` |
| `fullsend repos set-default defaults.vendor true` | `fullsend repos set defaults.vendor true` |

Differences to watch for in scripts:

- An empty value no longer deletes a key. It stores an empty string, or is rejected for non-string fields. Use `--unset`.
- Runner tags take a YAML or JSON list instead of a comma-separated string.
- `gitlab.runner_tags` can no longer be set; set `gitlab.agent_runner_tags` instead.
- Keys outside the old list, including `defaults.config.*`, `github.config.*`, `gitlab.config.*`, and repository entries, can now be set directly.
- `repos set` edits the file in place and validates the whole result, so it rejects an edit that leaves the manifest invalid (for example adding a GitLab entry before `gitlab.url` is set).

## See also

- [Getting Started](../guides/getting-started/) — Standard per-repo installation
- [Configuring GitLab](../guides/getting-started/configuring-gitlab.md) — GitLab getting-started guide
- [Operations](../guides/getting-started/operations.md) — Day-2 administration
- [CLI Internals](../guides/dev/cli-internals.md) — Command structure and implementation details
