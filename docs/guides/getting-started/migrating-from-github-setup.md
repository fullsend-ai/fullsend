---
sidebar_position: 6
---

# Migrating to manifest-driven `repos install`

`fullsend repos install` takes every persistent, non-secret setting and the
list of repositories it manages from `repos.yaml`. Command-line flags supply
only credentials, local vendoring artifacts, and controls for a single run.
This guide moves a repository to that model from either starting point:

- **`fullsend github setup`**, which configures one GitHub repository from
  flags and edits `.fullsend/config.yaml` in place.
- **Flag-driven `repos install`**, from scripts or CI jobs that pass
  `--roles`, `--fullsend-ref`, `--inference-auth`, `--forge`, or the other
  configuration flags that have been removed.

The procedure is the same in both cases: declare the values in `repos.yaml`,
supply credentials when you run install, preview, install, and verify.

**Target audience:** org maintainers and platform administrators who install
Fullsend with `repos install`. For manifest structure and day-2 operations,
see [Repo Management](repo-management.md).

## Prerequisites

- A fullsend CLI that includes the manifest-driven install
  ([#8219](https://github.com/fullsend-ai/fullsend/issues/8219)). Upgrade
  every CLI and pipeline that runs `repos install` against the same
  manifest. An older CLI still accepts the removed flags.
- A writable `repos.yaml`. When the manifest you install from is an HTTPS
  URL, make the edits in its source repository instead.
- Forge credentials: `GH_TOKEN` (or `gh auth login`) for GitHub, and
  `GITLAB_TOKEN` or `--gitlab-token` for GitLab.
- The secrets you supply at install time, such as an OpenAI API key or
  GitLab role tokens. See [Inputs that stay on the command line](#inputs-that-stay-on-the-command-line).
- For a Vertex project without a declared WIF provider: Application Default
  Credentials that can read the project through the GCP Resource Manager
  API, so install can derive the project number.

## What changes

| | `github setup` | Flag-driven `repos install` (before) | `repos install` (now) |
|---|---|---|---|
| Repository list | One `<owner/repo>` argument | Positional arguments added entries to `repos.yaml` | Entries declared in `repos.yaml`. Positional arguments only filter them |
| Persistent settings | Flags | Flags and `repos.yaml`, with different repeat-install behavior per flag | `repos.yaml` only |
| `.fullsend/config.yaml` | Generated, then edited key by key | Managed: rendered from `repos.yaml` with an ownership marker | Unchanged: managed |
| `FULLSEND_MINT_URL`, `FULLSEND_GCP_REGION`, GCP secrets | Copied from the effective configuration | Manifest `mint_url` and `--vertex-*` flags | Derived from values declared in the effective configuration, with `--vertex-project` / `--vertex-wif-provider` as credential inputs |
| Missing `repos.yaml` | Not used | Created by install | An error. Create it with `repos set` |

## Breaking changes

- **Configuration flags were removed from `repos install`.** `--roles`,
  `--fullsend-ref`, `--mint-url`, `--app-set`, `--inference-auth`,
  `--vendor` (and its hidden `--vendor-fullsend-binary` alias), `--signoff`,
  `--gitlab-url`, `--vertex-region`, and `--forge` now fail immediately. The
  error names the `repos.yaml` key to set and shows a `fullsend repos set`
  example. Nothing is read or written. See the
  [replacement table](#removed-flags-and-their-replacements).
- **Positional arguments no longer add repositories.** They select
  repositories already declared in `repos.yaml`. A selector that matches no
  declared repository (including an unrelated glob such as `other/*` next to
  a declared `acme/*`) fails the whole command before any manifest or forge
  write, even when other selectors match.
- **Install no longer creates `repos.yaml`.** A missing manifest is an
  error.
- **`FULLSEND_GCP_REGION` comes from configuration.** Declare
  `config.inference.region` in `repos.yaml` to make the region managed:
  install then repairs a variable that differs, and status reports the
  drift. Until you declare it, install keeps the existing
  `FULLSEND_GCP_REGION` (for example one set with the removed
  `--vertex-region`) and writes `global` only where the variable is
  missing.
- **Declared Vertex values become credential inputs.** An
  `inference.project` or `inference.wif_provider` in the managed
  configuration or preset is written to `FULLSEND_GCP_PROJECT_ID` /
  `FULLSEND_GCP_WIF_PROVIDER` on every install. A `--vertex-project` or
  `--vertex-wif-provider` value that disagrees with the declared value
  fails that repository before any write.
- **Mint URL conflicts fail.** When the manifest's explicit `mint_url` and a
  `mint_url` declared in the managed configuration or preset differ,
  install, dry run, and status report a conflict for that repository before
  any write.
- **`--fullsend-binary` and `--fullsend-source` need `vendor: true`.** They
  are accepted only when at least one selected repository resolves
  `vendor: true`.
- **Turning vendoring off cleans up.** If a GitHub repository was vendored
  and now resolves `vendor: false`, from `defaults.vendor` or its entry's
  `vendor`, install removes the stale binary, the vendor manifest, the
  `.defaults/` content, and the local reusable workflows. Fullsend's own
  callers are rewritten in the same scaffold commit. Install deletes only
  files Fullsend is known to have shipped; a file you created, even one
  the vendor manifest lists, is kept. If a workflow would still call a local reusable
  workflow, install keeps everything and reports the pending work. See
  [Turning vendoring off](../../cli/repos.md#turning-vendoring-off).

No configuration flags were added to `github setup` or any other command to
replace the removed ones.

## Removed flags and their replacements

Set the key by editing `repos.yaml` or with
[`fullsend repos set`](../../cli/repos.md#repos-set). Quote paths that
contain a repository selector.

| Removed flag | `repos.yaml` key | Valid at | Example |
|---|---|---|---|
| `--roles` | `config.roles` | `defaults`, `github` / `gitlab`, entry | `fullsend repos set defaults.config.roles '[triage, coder, review]'` |
| `--fullsend-ref` | `fullsend_ref` | `github` / `gitlab`, entry | `fullsend repos set 'github.repos["acme/api"].fullsend_ref' v1.2.3` |
| `--mint-url` | `mint_url`, or `config.mint_url` | `mint_url`: `github`, GitHub entry. `config.mint_url`: `defaults`, `github`, entry | `fullsend repos set github.mint_url https://mint.example.com` |
| `--app-set` | `app_set` | `github`, GitHub entry | `fullsend repos set github.app_set acme` |
| `--inference-auth` | `inference.auth` | `defaults`, `github` / `gitlab`, entry | `fullsend repos set defaults.inference.auth vertex-wif` |
| `--vendor`, `--vendor-fullsend-binary` | `vendor` | `defaults`, entry | `fullsend repos set defaults.vendor true` |
| `--signoff` | `signoff` | `defaults`, `github` / `gitlab`, entry | `fullsend repos set defaults.signoff true` |
| `--gitlab-url` | `gitlab.url` | `gitlab` | `fullsend repos set gitlab.url https://gitlab.example.com` |
| `--vertex-region` | `config.inference.region` (or the preset's `inference.region`) | `defaults`, `github` / `gitlab`, entry | `fullsend repos set defaults.config.inference.region us-east5` |
| `--forge`, and adding repositories with positional arguments | A repository entry | `github.repos`, `gitlab.repos` | `fullsend repos set 'github.repos["acme/api"]' '{}'` or `fullsend repos set 'gitlab.repos["group/project"]' '{}'` |

A GitLab entry needs `gitlab.url` first, so run
`fullsend repos set gitlab.url <url>` before adding the first GitLab
repository.

## Inputs that stay on the command line

These inputs are never written to `repos.yaml`:

| Purpose | Flags |
|---|---|
| Secrets and sensitive provisioning | `--openai-api-key`, `--vertex-project`, `--vertex-wif-provider`, `--gitlab-role-token role=token` (repeatable), `--gitlab-role-registry`, `--gitlab-token` (or `GITLAB_TOKEN`) |
| Local vendoring artifacts | `--fullsend-binary`, `--fullsend-source`. Valid only when a selected repository resolves `vendor: true`; otherwise install fails and points at `fullsend repos set defaults.vendor true` |
| One-run controls | `-f` / `--manifest`, `--dry-run`, `--direct`, `--concurrency`, `--force`, `--reactivate-schedules`, `--rotate-gitlab-roles`, `--rotate-gitlab-role`, `--rotate-gitlab-trigger-token`, `--rotate-gitlab-trigger-account` |

`--vertex-project` and `--vertex-wif-provider` are optional when the
effective configuration declares `inference.project` and
`inference.wif_provider`. Pass them when you prefer to keep the project out
of committed files: a declared value is written to `repos.yaml` and to the
repository's `.fullsend/config.yaml`.

## Where values live

### Mapping `github setup` flags

| `github setup` flag | `repos.yaml` destination |
|---|---|
| `<owner/repo>` | An entry under `github.repos` |
| `--runtime`, or the runtime prompt | `config.runtime` |
| `--agents` | `config.roles` |
| `--mint-url` | `config.mint_url`, or `github.mint_url` / the entry's `mint_url`. Declare one, or give both the same value |
| `--inference-provider` | `config.inference.provider` |
| `--inference-project` | `config.inference.project`, or `--vertex-project` at install time |
| `--inference-region` | `config.inference.region` |
| `--inference-wif-provider` | `config.inference.wif_provider`, or `--vertex-wif-provider` at install time |
| `--openai-audience`, `--openai-identity-provider-id`, `--openai-service-account-id` | `config.inference.openai.audience`, `.identity_provider_id`, `.service_account_id` |
| `--inference-gateway-url`, `--inference-gateway-audience`, `--inference-gateway-auth` | `config.inference.gateway.url`, `.audience`, `.auth` |
| `--inference-gateway-model id=api` | `config.inference.gateway.models` |
| `--inference-gateway-models-file` | `config.inference.gateway.models_file`, a path inside the repository. Commit the file yourself |
| `--config` | `config_base.source` (under `defaults` or the entry) |
| `--config-hash` | `config_base.sha256` |
| `--app-set` | `app_set` (under `github` or the entry) |
| `--fullsend-ref` | `fullsend_ref` (under `github` or the entry) |
| `--vendor` | `vendor` (under `defaults` or the entry) |
| `--fullsend-binary`, `--fullsend-source` | The same flags on `repos install` |
| `--direct`, `--dry-run` | The same flags on `repos install` |

`repos install` also needs `inference.auth` for every repository: use
`vertex-wif` for a Vertex setup, `openai-wif` when you passed the three
`--openai-*` flags, and `openai-api-key` when the repository uses
`FULLSEND_OPENAI_API_KEY`. See
[Why install requires `inference.auth`](#why-install-requires-inferenceauth).

### Precedence

Values resolve the same way on every install, dry run, and status run:

- **Manifest fields** (`fullsend_ref`, `mint_url`, `mint_mode`, `app_set`,
  `inference.auth`, `vendor`, `signoff`, `config_base`) resolve entry →
  forge section → `defaults`, at the levels each field supports.
- **Managed configuration** (`config`) resolves field by field, from lowest
  to highest priority: code defaults, the `config_base` preset,
  `defaults.config`, the forge section's `config`, then the entry's
  `config`. Scalars are replaced, and nested maps such as `inference`
  merge key by key. See
  [Repo Management — Managed configuration](repo-management.md#managed-configuration).
- **Flags** supply only credentials and per-run controls. They never
  override a `repos.yaml` value. A `--vertex-project` or
  `--vertex-wif-provider` that disagrees with a declared value is an
  error, not an override.

### Glob entries

A glob entry such as `acme/*` applies its settings to every repository it
matches. An explicit entry wins over a glob, and among globs the first match
wins.

```yaml
github:
  repos:
    - name: acme/*            # every acme repository
      config:
        runtime: pi
    - name: acme/api          # explicit entry: its own settings
      config:
        runtime: claude
```

`fullsend repos install acme/web` installs `acme/web` with the glob's
settings, even when the org listing omits it (for example a fork or an
archived repository), and adds no explicit entry. To give one repository
different settings, add an explicit entry for it.

### Infrastructure values

Install derives repository variables and secrets from each repository's
*effective configuration*: the managed configuration layered over the
declared preset.

| Value | Source |
|---|---|
| `FULLSEND_MINT_URL` (GitHub) | The effective `mint_url` when declared; otherwise the manifest `mint_url` / `mint_mode` (public mode defaults to `https://mint.fullsend.sh`) |
| `FULLSEND_GCP_REGION` (`vertex-wif` and `openai-wif`) | The effective `inference.region` when declared. Otherwise an existing variable is kept, and `global` is written only where it is missing and Vertex credentials are being written |
| `FULLSEND_GCP_PROJECT_ID` | `--vertex-project`; otherwise the effective `inference.project` |
| `FULLSEND_GCP_WIF_PROVIDER` | `--vertex-wif-provider`; otherwise the effective `inference.wif_provider`; otherwise derived from the project |
| OpenAI WIF identifiers (`openai-wif`) | The `FULLSEND_OPENAI_*` repository variables when set; otherwise `inference.openai` from the managed configuration, preset, or existing configuration. They are not secrets |

The full rules, including when the region variable is created and when
existing secrets are reused, are in
[`fullsend repos` — Effective infrastructure values](../../cli/repos.md#effective-infrastructure-values).

## Migrating a repository

Every scenario follows the same steps:

1. Declare the repository and its settings in `repos.yaml`.
2. Preview with `fullsend repos status --repo <repo>` and
   `fullsend repos install <repo> --dry-run`, passing the same credentials
   you will use for the real run.
3. Run `fullsend repos install <repo>` with those credentials.
4. Verify with `fullsend repos status --repo <repo>`, and review
   `git diff repos.yaml`.

`repos status` and `--dry-run` never write anything. When a configuration
file needs action, install stops before any manifest or forge write.

### Fresh repository

The repository has no Fullsend files.

1. Declare the repository and its settings:

   ```bash
   fullsend repos set defaults.inference.auth vertex-wif
   fullsend repos set 'github.repos["acme/api"]' '{}'
   fullsend repos set 'github.repos["acme/api"].config.runtime' pi
   ```

   `repos.yaml` now contains:

   ```yaml
   version: 1
   defaults:
     inference:
       auth: vertex-wif
   github:
     repos:
       - name: acme/api
         config:
           runtime: pi
   ```

2. Preview, then install:

   ```bash
   fullsend repos status --repo acme/api
   fullsend repos install acme/api --dry-run \
     --vertex-project "<gcp-project>" --vertex-wif-provider "<wif-provider>"
   fullsend repos install acme/api \
     --vertex-project "<gcp-project>" --vertex-wif-provider "<wif-provider>"
   ```

3. Verify and commit the manifest:

   ```bash
   fullsend repos status --repo acme/api
   git diff repos.yaml
   ```

   A first install without a preset records
   `config.create_issues.allow_targets.repos: [fullsend-ai/fullsend]` on
   the entry, and writes `github.fullsend_ref` when it is unset. Commit
   both.

For GitLab, set the instance URL and declare the project the same way:

```bash
fullsend repos set gitlab.url https://gitlab.com
fullsend repos set gitlab.inference.auth openai-api-key
fullsend repos set 'gitlab.repos["group/project"]' '{}'
fullsend repos install group/project --dry-run --openai-api-key "$OPENAI_API_KEY"
fullsend repos install group/project --openai-api-key "$OPENAI_API_KEY"
```

### Repository already in `repos.yaml`

The repository has an entry and a managed `.fullsend/config.yaml`, and a
script installs it with flags that are now removed. Move each flag value
into the manifest once, then drop the flag:

```bash
# Before
fullsend repos install acme/api --roles triage,review \
  --fullsend-ref v1.2.3 --signoff \
  --vertex-project "<gcp-project>" --vertex-region us-east5

# After: declare once
fullsend repos set 'github.repos["acme/api"].config.roles' '[triage, review]'
fullsend repos set 'github.repos["acme/api"].fullsend_ref' v1.2.3
fullsend repos set 'github.repos["acme/api"].signoff' true
fullsend repos set 'github.repos["acme/api"].config.inference.region' us-east5

# Then install with credentials only
fullsend repos install acme/api --dry-run --vertex-project "<gcp-project>"
fullsend repos install acme/api --vertex-project "<gcp-project>"
```

Declaring `config.inference.region` makes the region managed: install
compares `FULLSEND_GCP_REGION` with the declared value and repairs a
difference, and `--dry-run` and `repos status` show the change first. If
you leave it undeclared, install keeps the variable the old
`--vertex-region` flag set, but no longer checks it.

A flag such as `--signoff` used to apply to one run only. If the old
script passed it on every run, declare it. If it was a one-off, leave the
key out.

### Repository installed with `github setup` (markerless overlay)

`github setup` writes `.fullsend/config.yaml` without the managed-file
ownership marker. Install never overwrites such a file. It skips that repository
and reports `adoption required` for it before any write to it, including in a dry run;
the rest of the fleet still converges and the run exits non-zero.

1. Declare the repository with its manifest-level settings, such as
   `inference.auth`, `app_set`, `fullsend_ref`, and `vendor`:

   ```bash
   fullsend repos set 'github.repos["acme/api"]' '{"inference": {"auth": "vertex-wif"}}'
   ```

2. Read the adoption guidance:

   ```bash
   fullsend repos status --repo acme/api
   ```

   The repository is reported as `managed configuration (adoption required)`.
   The output (and the JSON `detail` field with `--json`) shows a proposed
   `repos.yaml` entry that carries every setting the existing file
   declares. It also shows how the file and the effective configuration
   would change.

3. Copy the settings to keep into the entry's `config`, or into
   `github.config` / `defaults.config` when they apply more widely. An
   omitted setting falls back to the preset and the code defaults. Include
   `mint_url` and the `inference` values: install now derives
   `FULLSEND_MINT_URL`, `FULLSEND_GCP_REGION`, and the GCP secrets from
   them, so a setup install whose project and provider lived only in its
   configuration migrates without `--vertex-*` flags.

   ```yaml
       - name: acme/api
         inference:
           auth: vertex-wif
         config:
           runtime: pi
           roles: [triage, review]
           inference:
             project: my-gcp-project
             region: us-east5
             wif_provider: projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc
   ```

4. Adopt the file: add the two marker lines at the top of
   `.fullsend/config.yaml` in a pull request or direct commit:

   ```yaml
   # This file is managed by 'fullsend repos': repos-config-v1
   # Do not edit this file directly; changes will be overwritten.
   ```

   Do not delete the file instead. Deleting it drops restrictions that
   exist only in the file, such as `kill_switch: true`, until install
   converges. It also hides those restrictions from the safety check below.

5. Run `fullsend repos status --repo acme/api` again. It now reports
   ordinary drift, or none. Then preview and install:

   ```bash
   fullsend repos install acme/api --dry-run
   fullsend repos install acme/api
   ```

Install rejects a rewrite that makes the effective configuration less
restrictive than the current one unless `repos.yaml` declares that
relaxation. The compared keys are `kill_switch`, `roles`,
`allowed_remote_resources`, agent `enabled: false` suppressions,
`create_issues.allow_targets`, `inference.gateway.url`, and
`inference.gateway.audience`. The output names the affected keys.

After the repository is in `repos.yaml`, stop running `github setup` on it.
A setup run that rewrites `.fullsend/config.yaml` removes the ownership
marker, and the next install stops until the file is adopted again.

### Repository with an existing preset

`github setup --config` commits the preset as `.fullsend/config.base.yaml`.
With no preset declared for the repository, that file blocks install
before any write. Install never deletes it.

1. Declare the preset that produced the file, using the source and digest
   you passed to `--config` and `--config-hash`:

   ```bash
   fullsend repos set 'github.repos["acme/api"].config_base' \
     '{"source": "https://example.com/presets/org.yaml", "sha256": "<sha256-hex>"}'
   ```

   Use `defaults.config_base` instead when every repository shares the
   preset.

2. `github setup --config` also wrote a markerless `.fullsend/config.yaml`.
   Adopt it as described in the previous section. `repos status` reports
   both files in one run.

3. Check the preset's infrastructure values. A `mint_url` or
   `inference.region` / `project` / `wif_provider` in the preset now feeds
   `FULLSEND_MINT_URL`, `FULLSEND_GCP_REGION`, and the GCP secrets. If the
   preset's `mint_url` differs from `github.mint_url`, install reports a
   conflict: make them match or unset one, for example
   `fullsend repos set --unset github.mint_url`.

4. Preview and install:

   ```bash
   fullsend repos status --repo acme/api
   fullsend repos install acme/api --dry-run
   fullsend repos install acme/api
   ```

When the fetched preset matches the installed file byte for byte, install
leaves it alone. A different preset replaces the whole base file, after the
same safety comparison.

## Repeat installs

With an unchanged manifest, a repeat install changes nothing, with one
exception: when a project is declared (`inference.project` in the effective
configuration) or passed with `--vertex-project`, every install rewrites
`FULLSEND_GCP_PROJECT_ID` and `FULLSEND_GCP_WIF_PROVIDER`. Secret values
cannot be read back to compare, so they are always refreshed from the
supplied values. When neither supplies a project, the existing secrets are
reused.

Install writes to `repos.yaml` itself only in two cases, both of which
predate this change:

- On a first install with no preset and no `create_issues` declaration, it
  records `config.create_issues.allow_targets.repos: [fullsend-ai/fullsend]`
  on the repository's entry. For a repository covered only by a glob, this
  adds an explicit entry.
- After a first install, it writes the CLI's release to
  `github.fullsend_ref` or `gitlab.fullsend_ref` when that key is unset.

## Why install requires `inference.auth`

`github setup` can scaffold a repository with no inference credentials at
all. `repos install` deliberately does not. Every selected repository must
resolve `inference.auth`, and that method's credentials must already exist
on the repository or be supplied by flags or declared configuration.
Otherwise the repository fails before any write.

A fleet install converges many repositories without anyone watching each
one. Requiring an explicit method means install provisions exactly the
credentials each repository uses, removes the other method's leftover
secrets, and fails early rather than leaving agents that cannot reach a
model. To keep the previous Vertex behavior everywhere, run:

```bash
fullsend repos set defaults.inference.auth vertex-wif
```

## Troubleshooting

- **A removed flag fails.** The error names the key to set and gives a
  `fullsend repos set` example. Declare the value, then re-run without the
  flag.
- **A selector matches no declared entry.** Install lists the undeclared
  selectors. Add each repository with
  `fullsend repos set 'github.repos["owner/repo"]' '{}'` (or the
  `gitlab.repos` form), or fix the selector's spelling.
- **The manifest does not exist.** Create it with `fullsend repos set`. The
  first edit writes `version: 1`.
- **Mint URL conflict.** The manifest `mint_url` and the configuration or
  preset `mint_url` differ. Make them match or unset one.
- **Vertex input conflict.** A `--vertex-project` or `--vertex-wif-provider`
  value disagrees with the declared `inference.project` or
  `inference.wif_provider`. Drop the flag or change the declared value.
- **A WIF provider without a project.** Declare `inference.project` next to a
  declared `inference.wif_provider`; `--vertex-project` alone does not satisfy
  a declared provider. Without a declared provider, pass `--vertex-project`
  together with `--vertex-wif-provider`.
- **Project number lookup fails.** Install derives the WIF provider from the
  project through the GCP Resource Manager API. Declare
  `inference.wif_provider` or pass `--vertex-wif-provider` to skip the
  lookup.
- **`FULLSEND_GCP_REGION` drift.** Status reports region drift only when a
  region is declared. Make the declared `config.inference.region` (or the
  preset's `inference.region`) match the region the repository should use,
  then re-run install to repair the variable.
- **`--fullsend-binary` or `--fullsend-source` is rejected.** No selected
  repository resolves `vendor: true`. Run
  `fullsend repos set defaults.vendor true`, or set `vendor` on the entry.

## See also

- [Repo Management](repo-management.md) — manifest structure, presets, and managed configuration
- [`fullsend repos`](../../cli/repos.md) — command and flag reference
- [`fullsend github`](../../cli/github.md) — `github setup` flag reference
- [Config Reference](../../reference/config-reference.md) — every `config` field
