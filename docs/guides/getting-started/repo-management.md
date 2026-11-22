---
sidebar_position: 5
---

# Repo Management

Manage per-repo fullsend installations at scale using a declarative
`repos.yaml` manifest. The `fullsend repos` command group provides bulk
install, status checking, drift detection, configuration sync, and
version upgrades across multiple repos, GitHub orgs, and GitLab groups.

**Target audience:** Platform administrators (SRE/DevOps) managing
fullsend across an organization. For a single repository, GitHub owners
should use `fullsend github setup` (see [Configuring GitHub](configuring-github.md));
GitLab project owners declare the project in `repos.yaml` and run
`fullsend repos install` (see [Configuring GitLab](configuring-gitlab.md)).
To move an existing `github setup` installation, or scripts that pass
configuration flags to `repos install`, see
[Migrating to manifest-driven `repos install`](migrating-from-github-setup.md).

## Prerequisites

- **fullsend CLI** installed (see [releases](https://github.com/fullsend-ai/fullsend/releases),
  or run [`fullsend self-update`](../../cli/self-update.md) on v0.55.0 or later)

The remaining prerequisites are forge-specific:

**GitHub:**

- **GitHub access** — admin or write access to the target repositories
- **`gh` CLI** authenticated with the required OAuth scopes (see [OAuth scope reference](../infrastructure/advanced-setup.md#oauth-scope-reference))
- **Vertex prerequisites** — if any agent will use Vertex, complete GCP WIF provisioning (`fullsend inference provision`) separately. For self-managed mints, mint enrollment (`fullsend mint enroll`) is also required. The hosted community mint needs no enrollment — install the shared Apps and use the CLI defaults. When multiple repos share the same GCP project, existing inference secrets are reused automatically. See [Mint administration](../infrastructure/mint-administration.md) and [Advanced setup](../infrastructure/advanced-setup.md).

An OpenAI-only repository can be installed without GCP inference inputs. Each
agent's provider is selected when it runs; see
[OpenAI Workload Identity](../infrastructure/openai-workload-identity.md).

**GitLab:**

GitLab does not use `gh` or mint enrollment.
`fullsend inference provision --forge gitlab` is optional; it creates a
per-project Vertex WIF provider. See [Configuring GitLab § Prerequisites](configuring-gitlab.md#prerequisites)
for the GitLab token and runner requirements. A GCP inference project is
needed only for Vertex agents.

## Getting started

### Creating a manifest from scratch

`repos.yaml` is the single source of persistent, non-secret install
settings and of the repositories fullsend manages. Create it and declare
repositories with [`repos set`](#editing-the-manifest-from-the-command-line),
or write the YAML by hand:

```bash
fullsend repos set defaults.inference.auth vertex-wif
fullsend repos set 'github.repos["acme/api"]' '{}'
fullsend repos set 'github.repos["acme/web"]' '{}'
fullsend repos install --direct --vertex-project "<gcp-project>"
```

The first `repos set` creates `repos.yaml` (or the path given by `-f`)
with `version: 1`. `repos install` does not create the manifest or add
entries: a missing manifest is an error, and positional arguments only
select repositories that are already declared.

### Multi-forge manifests

Each platform (`github`, `gitlab`) is a top-level key containing its
infrastructure config and repos list. Repos are grouped under their
platform — no per-entry forge selector is needed. Forgejo repositories
are declared separately under `forgejo.instances`; see
[Forgejo instances](#forgejo-instances) below.

```yaml
version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v2.5.0
  repos:
    - name: acme/api-server
    - name: acme/web-frontend

gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v2.5.0
  repos:
    - name: gitlab-group/project
    - name: gitlab-group/subgroup/nested-project
```

GitHub repos use a token mint for authentication. The
`github.mint_mode` field controls the default mint URL:
`public` (default) uses `https://mint.fullsend.sh` when no explicit
`mint_url` is set; `private` requires a mint URL, which can come from
`mint_url` (in the `github` section or an entry), `config.mint_url`, or a
`config_base` preset. Both
`mint_mode` and `mint_url` can be overridden per-repo. A `mint_url`
declared in the managed configuration (`config.mint_url`) or the preset
takes precedence for the `FULLSEND_MINT_URL` variable. When it differs
from an explicit manifest `mint_url`, install and status report a
conflict for that repository; see
[Effective infrastructure values](../../cli/repos.md#effective-infrastructure-values).

#### Forgejo instances

Repositories on one or more Forgejo instances are declared under
`forgejo.instances`. Each instance has an `alias`, its base `url`, and
its `repos` as `owner/repo` paths relative to that URL. The same path can
be declared on several instances because a repository is identified by
its alias plus its path:

```yaml
forgejo:
  instances:
    - alias: home
      url: https://git.example.com/forgejo
      repos:
        - name: org/app
    - alias: codeberg
      url: https://codeberg.org
      repos:
        - name: org/app
```

- **Credentials.** Each instance reads its API token from
  `FULLSEND_FORGEJO_<NORMALIZED_ALIAS>_TOKEN` (the alias uppercased, with
  each run of non-alphanumeric characters replaced by one underscore, so
  `code-berg` reads `FULLSEND_FORGEJO_CODE_BERG_TOKEN`). Tokens are never
  stored in `repos.yaml`.
- **Selectors.** `forgejo:<alias>/<owner>/<repo>` selects a repository on
  one instance (the `<owner>/<repo>` part may be a glob). A bare
  `owner/repo` selects a Forgejo repository only when it is declared on
  exactly one instance and no `github`/`gitlab` declaration also matches;
  otherwise the command fails and lists the qualified choices.
- **Uninstall confirmation.** A selection that spans several repositories
  is confirmed once, as a whole, before anything is torn down.
- **Compatibility.** `forgejo` is a new key in `repos.yaml`, and the
  manifest parser rejects unknown keys. A fullsend binary from before
  Forgejo support fails to load a manifest that contains `forgejo:`, so
  upgrade every machine and CI job that reads the manifest before adding
  the section. Manifests without it are unaffected.

See [Forgejo instances](../../cli/repos.md#forgejo-instances) in the CLI
reference for the full lifecycle and selector rules.

`github.app_set` names the GitHub App set (apps named `{app-set}-{role}`)
and is persisted as the `FULLSEND_APP_SET` repository variable, mirroring
`fullsend github setup --app-set`. It is GitHub-only: setting `app_set`
under `gitlab` or on a GitLab repo entry is a validation error. The value
must be lowercase alphanumeric with optional hyphens (max 23 characters).
When omitted, repos get the built-in default `fullsend-ai`. Like other
scalar fields it can be set at the platform level and overridden per-repo
(for example `fullsend repos set github.app_set acme`). Reruns and
convergence preserve an existing custom `FULLSEND_APP_SET`
rather than reverting it to the built-in default, and repair the variable
when it has drifted (when `app_set` is declared) or is missing (older
installs). The implementation does not weaken exact bot-identity matching.

For GitLab repos, set the `GITLAB_TOKEN` environment variable or pass
`--gitlab-token` to `fullsend repos` subcommands. Manifest-driven commands
(`repos install` and `repos status`) require `gitlab.url` whenever GitLab
repos are present, including gitlab.com; there is no manifest default. Set
it with `fullsend repos set gitlab.url <url>` before declaring the first
GitLab repository.

`fullsend run` does not read the manifest. It reads the GitLab URL from
the first one set of `FULLSEND_GITLAB_URL`, `GITLAB_API_URL` and
`CI_SERVER_URL`. These variables never override `gitlab.url` in
manifest validation. `fullsend run` limits `GITLAB_TOKEN` in the
sandbox to that URL's host and port. It stops before setup if the URL
is invalid. With the stock GitLab agents, it also stops if no URL is
set. For a local run, set it:

```bash
export FULLSEND_GITLAB_URL=https://gitlab.example.com
```

For each stop and its fix, see [Self-Hosted GitLab Instances](configuring-gitlab.md#self-hosted-gitlab-instances).

Self-hosted instances that use a private CA need runner `tls-ca-file`
plus separate sandbox-host trust — see [Private CA (self-hosted GitLab)](operations.md#private-ca-self-hosted-gitlab).

Per-repo fields inherit from the platform-level default when omitted.
To explicitly stop a field from inheriting, set it to the literal value
`none`. For example, `fullsend_ref: none` prevents the repo from
inheriting the platform-level `fullsend_ref`:

```yaml
github:
  fullsend_ref: v2.5.0
  repos:
    - name: acme/api
    - name: acme/legacy
      fullsend_ref: none  # does not inherit v2.5.0
```

The `none` sentinel works for string fields (`fullsend_ref`,
`mint_url`, `mint_mode`, `config_base.source`, `config_base.sha256`). Fields
inside `config`, such as `allowed_remote_resources`, follow their own merge
rules instead; see [Managed configuration](#managed-configuration). For `app_set`, `none` resets the effective
value to the built-in default `fullsend-ai` rather than disabling the
field (an app set is always required to resolve App slugs).

### Inference authentication

Every repo must resolve an explicit inference authentication method,
`inference.auth`: `vertex-wif` (Vertex AI through GCP Workload Identity
Federation), `openai-api-key`, or `openai-wif` (OpenAI through Workload
Identity Federation, GitHub only). There is no implicit default. A
repo with no selection is reported as a configuration error by
`repos install` and `repos status`, before any forge change is made for
that repo. The exception is a repo whose effective configuration (for
example `defaults.config.inference.gateway`, or the `config_base`
preset) declares a complete `inference.gateway` block. The gateway
authenticates on its own, so such a repo needs no `inference.auth` and
install provisions no provider credentials for it (see
[Inference gateway route](../../cli/repos.md#inference-gateway-route)).
Uninstall does not need the setting: it removes every fullsend-managed
inference credential whether `inference.auth` is set, valid, or
missing.

The selection can be set under `defaults`, in each forge section, and on
repo or glob entries. It resolves entry → forge section → `defaults`
using the normal entry matching rules (an explicit entry wins over a glob),
so a repo-level value never affects its siblings:

```yaml
version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  repos:
    - name: acme/api
    - name: acme/openai-*
      inference:
        auth: openai-api-key
gitlab:
  url: https://gitlab.example.com
  inference:
    auth: openai-api-key
  repos:
    - name: group/project
```

Only the selection is stored under the manifest-level `inference` key, and
`none` is not a valid value. Secrets are supplied on the command line
(`--openai-api-key` for `openai-api-key`). Vertex values come from
`--vertex-project` / `--vertex-wif-provider` or from `inference.project` /
`inference.wif_provider` declared in the managed configuration. A declared
`config.inference.region` makes `FULLSEND_GCP_REGION` managed; without one,
install keeps an existing variable and writes `global` only where it is
missing. `repos install`
provisions only the credentials of each repo's selected method; see
[Inference credentials](../../cli/repos.md#inference-credentials) and
[Effective infrastructure values](../../cli/repos.md#effective-infrastructure-values).

`openai-wif` needs no fullsend-managed secret. Instead, each repo needs a
complete set of OpenAI WIF identifiers: the `FULLSEND_OPENAI_*` repository
variables, or `inference.openai` in its layered `.fullsend/config.yaml`
(see [OpenAI Workload Identity](../infrastructure/openai-workload-identity.md#4-tell-fullsend-the-three-identifiers)).
`repos install` fails before any write when the identifiers are missing
or partial. GCP secrets are optional and kept for Vertex sub-agents.
GitLab repos cannot select `openai-wif`.

Set the selection with `repos set` at the level you want:
`defaults.inference.auth`, `github.inference.auth`,
`gitlab.inference.auth`, or a single entry such as
`'github.repos["acme/api"].inference.auth'`. `repos install` has no flag
for it. See
[fullsend repos § Inference authentication selection](../../cli/repos.md#inference-authentication-selection)
for the full precedence rules, and for why install requires a selection
when `github setup` does not.

**Upgrading:** manifests written before `inference.auth` existed are not
treated as Vertex. Add the selection your repos actually use before
upgrading. For example, to keep the previous Vertex WIF behaviour
everywhere:

```bash
fullsend repos set defaults.inference.auth vertex-wif
```

**Shared manifests:** older CLI versions reject unknown manifest fields, so
a `repos.yaml` that contains `inference.auth` cannot be parsed by a CLI
that predates the key. When several people or pipelines share one
manifest, upgrade every consumer's CLI before committing the manifest edit
that adds `inference.auth`.

### Configuration presets

`repos.yaml` can declare a configuration preset so fleet installs
reproduce `github setup --config`. The preset is a local file or HTTPS
URL written byte-for-byte as `.fullsend/config.base.yaml`.
`.fullsend/config.yaml` remains the repository-local overlay and is not
merged with the preset.

```yaml
version: 1
defaults:
  config_base:
    source: https://example.com/presets/org.yaml
    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
github:
  repos:
    - name: acme/api          # inherits the fleet preset
    - name: acme/special
      config_base:
        source: ./special.yaml  # per-repo override (path is relative to repos.yaml's directory)
        sha256: none            # do not apply the fleet hash to this source
    - name: acme/legacy
      config_base:
        source: none            # disable inheritance; desired state is no base file
```

`config_base.sha256` is optional. When set, it must be a 64-character SHA-256
hex digest of the fetched content, matching `github setup --config-hash`.
A per-repo `config_base.source` override still inherits
`defaults.config_base.sha256` unless you set `config_base.sha256` on that
entry (`none` skips validation). A hash mismatch or invalid source fails
before any files are written. Remote presets without a hash are accepted,
but content integrity is not verified. Local preset paths are resolved
relative to the directory containing `repos.yaml`, not the process working
directory, and must stay within that directory (`../` paths that escape it
are rejected). A manifest loaded from an HTTPS URL must declare preset
sources as HTTPS URLs; local preset paths are not allowed in that case.

Changing the declared preset replaces the complete base file. Repeated
installs are idempotent when the desired bytes already match. If no
preset resolves for a repository (none is declared, or `source: none`
disables inheritance), the desired state is no
`.fullsend/config.base.yaml`. An existing base file is then drift, not
an unmanaged file to keep: `repos status` reports it for every manifest
repository (including one with no fullsend components yet), and fresh and
repeat installs, including `--dry-run`, stop for that repository before
any writes. A zero-byte base file counts as existing. To resolve it,
declare a `config_base` source (and optional `sha256`) that matches the
file you want, or remove the file from the repository. Install never
deletes it automatically. Leaving `config_base` out needs no `none`
marker. The same path applies to GitHub and GitLab.

With a declared preset, install first validates that an existing base is a
well-formed per-repo configuration (a malformed or non-per-repo file is an
error, never silently replaced), compares its bytes with the preset, and
shows the replacement in `--dry-run`. Before replacing the base, install
compares the current effective configuration (installed overlay over
installed base) with the proposed one (proposed overlay over the declared
preset). If the replacement would implicitly relax a restriction the
overlay does not declare — `kill_switch`, `roles`,
`allowed_remote_resources`, agent suppressions,
`tracker.<provider>.allowed_targets`, `inference.gateway.url`,
`inference.gateway.audience`, `artifacts.content`,
`artifacts.by_source.<source>`, `progress.verbosity`, or
`progress.by_source.<source>` — that repository fails before any write and
the output names the keys. Changing a preset source alone is not an
explicit declaration of a less-restrictive setting.

Agent roles follow the same layering: declare `config.roles` only when a
repository must keep a list that differs from the preset (or from the code
default `triage,coder,review,fix,retro,prioritize` when there is no
preset). An undeclared `roles` key is not written to the overlay, so the
preset's own roles take effect.

A preset can also declare infrastructure values. Its `mint_url` and
`inference.region` / `inference.project` / `inference.wif_provider`
feed `FULLSEND_MINT_URL`, `FULLSEND_GCP_REGION`, and the GCP secrets
unless the managed configuration overrides them. For GitLab `vertex-wif`
repositories it can also supply `inference.wif_audience` and
`inference.service_account`; see
[Effective infrastructure values](../../cli/repos.md#effective-infrastructure-values).

### Managed configuration

`repos.yaml` can also declare a sparse managed `.fullsend/config.yaml` in
`defaults.config`, `github.config`, `gitlab.config`, and each repository
entry's `config`. Every section uses the same schema as per-repo
`config.yaml`, including `runtime` and `allowed_remote_resources`, except
`trace_context`, which only a concrete repository entry accepts (see
[Config Reference — `trace_context`](../../reference/config-reference.md#trace_context)).
Each section is sparse: declare only the values you want to set.

```yaml
version: 1
defaults:
  config:
    runtime: pi
    kill_switch: false
    inference:
      region: us-east1
github:
  config:
    allowed_remote_resources:     # GitHub repositories only
      - https://example.com/agents/
  repos:
    - name: acme/api              # inherits defaults.config, then github.config
    - name: acme/special
      config:
        kill_switch: true         # repository values win
        allowed_remote_resources: []   # explicit empty list: deny all
    - name: acme/plain            # no config key: still managed (sparse, marker only)
gitlab:
  url: https://gitlab.example.com
  config:
    runtime: claude               # GitLab repositories only
  repos:
    - name: group/project
```

Every repository selected from the manifest gets a managed
`.fullsend/config.yaml`. Neither `defaults.config` nor a repository `config`
is required. A repository with no `config` gets a file that holds only the
ownership marker. The managed file holds only the values you declare: code
defaults and the preset are not copied into it. `config` is this managed
overlay. `config_base` is a separate preset that install copies to
`.fullsend/config.base.yaml` (see [Configuration presets](#configuration-presets)).

#### How the layers combine

Fullsend resolves each repository's configuration from these layers, in
order. A later layer wins over an earlier one:

1. Code defaults.
2. The `config_base` preset.
3. `defaults.config`.
4. The forge section's `config` (`github.config` or `gitlab.config`). It
   applies only to repositories on that forge.
5. The repository entry's `config`, including entries that a glob matches.

Each field keeps its normal layered merge rule:

- A scalar replaces the inherited value.
- Nested maps merge key by key.
- A non-empty `allowed_remote_resources` list is added to the inherited
  prefixes. An explicit empty list (`[]`) replaces them and denies every
  remote resource.
- `tracker` replaces the inherited value as a whole. Its lists do not merge
  across layers (see [Issue tracker](#issue-tracker)).

Manifest validation rejects:

- Unknown fields and invalid values. The error names the section.
- A non-empty `allowed_remote_resources` list in a more specific layer than an
  explicit empty one. The managed file cannot record that reset, so declare
  the full list in a single layer.
- An agent's `harness:` block set in more than one of the `defaults`, forge
  section, and repository `config` layers. These layers render into one
  `config.yaml`, so set the block in one layer (see the
  [config reference](../../reference/config-reference.md#agents)).
- `runtime` or `allowed_remote_resources` as sibling keys under `defaults` or
  a repository entry. The error names the replacement path under `config`.
  There is no automatic migration:

```yaml
# Invalid — rejected at manifest load:
#   defaults.runtime is no longer supported; move it to defaults.config.runtime
defaults:
  runtime: pi
  allowed_remote_resources: []
```

#### Issue tracker

`config.tracker` tells agents which issue tracker the repository uses and
where, beyond the repository itself, they may create issues. It is the same
`tracker` field as in a per-repo `config.yaml`, and you can set it in any
managed layer. The
[Config Reference — `tracker`](../../reference/config-reference.md#tracker)
lists every key.

`config.tracker` controls:

- **Issues that agents create in other places.** `allowed_targets` lists the
  other repositories, groups, or projects where agents may create issues. For
  example, the [retro agent](../../agents/retro.md) files its improvement
  proposals as issues, and the [triage agent](../../agents/triage.md) can file
  a prerequisite issue in an upstream repository. On Jira, `allowed_targets`
  also restricts which projects' existing issues `fullsend issues transition`
  can change the workflow status of.
- **The default tracker for `fullsend issues`.** The selected provider is the
  default `--tracker` value of the
  [issue commands](../user/issues-commands.md). With no provider selected,
  every `fullsend issues` command needs `--tracker`.
- **The Jira connection.** `tracker.jira` holds the Jira site, project, and
  poller settings (see [Jira tracker](#jira-tracker)).

Apart from the Jira `fullsend issues transition` check above, `allowed_targets`
limits only where agents create issues. It does not limit
which issues agents read or search. For example, triage searches for
duplicates and related issues whatever `allowed_targets` lists.

Agents can always create issues in the repository they run in, so that
repository needs no entry in `allowed_targets`. When `allowed_targets` is empty or
missing, agents create issues only in that repository. For example, the triage
agent then puts a draft prerequisite issue in its comment for a person to
file.

`<provider>` in `config.tracker.<provider>` is one of three fixed keys. It is
not a hostname or a pattern:

| Provider | Issues live in | `allowed_targets` keys |
|----------|----------------|------------------------|
| `github` | GitHub repositories | `orgs` (every repository in the organization), `repos` (`owner/name`) |
| `gitlab` | GitLab projects on the GitLab instance that hosts the repository | `groups` (projects in the group and its subgroups), `repos` (full project path) |
| `jira` | Jira projects on the site in `tracker.jira.base_url` | `projects` (Jira project keys) |

Set at most one provider. Validation rejects any other key under `tracker`,
and a `tracker` that holds two providers. The provider does not need to match
the forge: a repository on GitHub can track its issues in Jira. Because a more
specific layer replaces the whole `tracker` value, a repository entry that sets
`tracker` drops the provider and targets it would otherwise inherit.

##### Defaults on first install

With no `tracker` in any layer, no provider is selected and agents create
issues only in the repository they run in. To keep the manifest the source of
truth, the first install of a repository records a forge-specific default in
that repository's manifest entry:

| Repository forge | Value recorded in the manifest entry | Agents can create issues in |
|------------------|--------------------------------------|-----------------------------|
| GitHub | `config.tracker.github.allowed_targets.repos: [fullsend-ai/fullsend, fullsend-ai/agents]` | The repository, plus the platform and agent repositories, so retro proposals and triage prerequisites can reach the repository that owns a change |
| GitLab | `config.tracker.gitlab.allowed_targets: {}` | The project only. Add other projects or groups yourself |

Install records the default only when all of these are true:

- The repository has no fullsend installation, no `.fullsend/config.yaml`,
  and no `.fullsend/config.base.yaml`.
- No `config_base` preset is declared. A preset keeps its own `tracker`.
- No layer of the manifest sets `tracker`. Install never overrides a value you
  declare.

For a repository that only a glob covers, install adds an explicit entry,
copied from the glob, so that sibling repositories are not affected. When the
manifest is read-only, install fails and prints the exact edit to make (see
[Read-only manifests](#read-only-manifests)). Existing installations keep
their targets on upgrade.

The retro and triage post-scripts currently read the targets only from
`.fullsend/config.yaml`, not from the preset. Until
[agents#1728](https://github.com/fullsend-ai/agents/issues/1728) is fixed,
declare the targets these post-scripts need in `config.tracker` in the
manifest, even when you use a preset.

##### GitHub tracker

List organizations, repositories, or both. A target is allowed when its
organization is in `orgs` or the repository is in `repos`:

```yaml
github:
  repos:
    - name: acme/api
      config:
        tracker:
          github:
            allowed_targets:
              orgs: [acme]
              repos: [fullsend-ai/fullsend, fullsend-ai/agents]
```

The list replaces the default, so keep `fullsend-ai/fullsend` and
`fullsend-ai/agents` if you still want retro proposals filed there.

##### GitLab tracker

The `gitlab` provider is not limited to gitlab.com. It means the GitLab
instance that hosts the repository: gitlab.com, or the self-hosted server in
the manifest's `gitlab.url`. Targets are group and project paths on that
instance. You cannot name a project on a different GitLab host:

```yaml
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/project
      config:
        tracker:
          gitlab:
            allowed_targets:
              groups: [platform]            # platform/... and its subgroups
              repos: [tools/sub/project]    # full project path
```

##### Jira tracker

Select `jira` to track issues in Jira while the code and pull requests stay
on GitHub or GitLab. `jira` replaces the forge provider, so the default GitHub
targets no longer apply and you cannot also list GitHub or GitLab targets.
Agents create Jira issues in `tracker.jira.project` or in a project listed in
`allowed_targets.projects`:

```yaml
github:
  repos:
    - name: acme/api
      config:
        tracker:
          jira:
            base_url: https://example.atlassian.net
            project: PROJ
            allowed_targets:
              projects: [OTHER]
```

Jira credentials are repository secrets, never part of `tracker.jira`. For the
connection settings, polling, and credentials, see
[`tracker.jira` connection and polling settings](../../reference/config-reference.md#trackerjira-connection-and-polling-settings)
and [Jira Integration](../user/jira-integration.md).

#### The managed file

Every managed `.fullsend/config.yaml` begins with a stable ownership
marker and otherwise contains only the declared values:

```yaml
# This file is managed by 'fullsend repos': repos-config-v1
# Do not edit this file directly; changes will be overwritten.
kill_switch: true
runtime: pi
inference:
  region: us-east1
```

When the manifest is loaded from an HTTPS URL (`--manifest
https://...`), a third comment line after the marker names that
manifest, so readers of the target repository can find where to change
the file:

```yaml
# This file is managed by 'fullsend repos': repos-config-v1
# Do not edit this file directly; changes will be overwritten.
# Source manifest: https://raw.githubusercontent.com/acme/infra/main/repos.yaml
```

Install records this line only for raw-file URLs on public source hosts,
where the path holds repository coordinates rather than a credential:

- `raw.githubusercontent.com/<owner>/<repo>/<ref>/<path>`
- `github.com/<owner>/<repo>/raw/<ref>/<path>`
- `gitlab.com/<group>/<project>/-/raw/<ref>/<path>`

The URL must have no port, and each path segment must use only letters,
digits, and `._~-`. Install writes only the scheme, host, and path. It removes
userinfo, the query string, and the fragment. For any other URL, such as a
self-hosted server, a URL with a port, or a percent-escaped segment, install
omits the line, because a credential in the path cannot be told apart from a
directory name. The line becomes part of the target repository's Git history,
which may be public, so do not put a credential in the path of a manifest URL
on a recognized host. A manifest loaded from a local path records no source
line, because its location on the operator's machine means nothing to other
readers.

The line is informational. `repos status` and `repos install` ignore it when
they compare files, so a changed manifest URL, or a switch between a URL and a
local path, is not drift and does not rewrite the file. Install updates the
line the next time a configuration change rewrites the file. A file without
the line still carries the ownership marker, so it never requires adoption.

Once a file carries the marker:

- `repos install` writes the canonical sparse `.fullsend/config.yaml`. For an
  explicitly pinned `fullsend_ref`, install renders it in the configuration
  schema that ref reads, and leaves the file unchanged with an error when it
  cannot determine that schema (see
  [Configuration schema of a pinned ref](../../cli/repos.md#configuration-schema-of-a-pinned-ref)).
- `repos status` reports any whole-file difference, even a single key, as
  drift.
- Convergence rewrites the file deterministically, unless the
  [safety gate](#safety-gate-for-less-restrictive-changes) blocks the change.

A malformed or non-per-repo file, or a read error, fails with a clear error,
and install never replaces that file. Install manages
`.fullsend/config.base.yaml` as described under
[Configuration presets](#configuration-presets).

#### Adopting an existing file

An existing `.fullsend/config.yaml` without the ownership marker needs
adoption, even when the repository has no `config` key. An empty file counts
as a file without the marker. For such a repository:

- `repos status` reports `managed configuration (adoption required)` instead
  of ordinary drift. Its output (table and JSON `detail`) shows:
  - the proposed `repos.yaml` entry, which carries every setting the existing
    file declares, including values equal to the code defaults
  - the difference between the existing file and the managed file that
    adoption would produce
  - the change to the effective layered configuration: the existing overlay
    over the installed base, against the managed overlay over the base the run
    would leave in place. This shows any setting that adoption would drop, or
    that changes only through the base layer.
- `repos install` skips the repository and leaves the file untouched. It
  prints the same guidance, lists the repository as `FAILED` in the summary,
  converges the other selected repositories, and exits non-zero. A dry run
  does the same.

To adopt the file, edit it to carry the marker, or replace it with the
rendered managed body. `repos status` reports a markerless file that is
malformed or is not a per-repo configuration as an error instead.

#### Safety gate for less restrictive changes

Before it writes a managed file, install compares the candidate and the
current effective configuration through the full overlay → base →
code-defaults chain. It rejects a candidate that is less restrictive, unless
the manifest explicitly declares that change. A blanket adoption
acknowledgement is not enough. The gate compares:

- `kill_switch` and `roles`
- `allowed_remote_resources`
- agent `enabled: false` suppressions
- `tracker.<provider>.allowed_targets`
- `inference.gateway.url` and `inference.gateway.audience`
- `artifacts.content` and `artifacts.by_source.<source>`
- `progress.verbosity` and `progress.by_source.<source>`

Omitted keys fall through to the next layer and are not treated as unset. An
explicit empty `allowed_remote_resources: []` stays deny-all. Any change to
an `inference.gateway` field must be declared in the manifest. This includes a
preset that adds a gateway to an established installation that has none.
Status and install output name the affected keys.

Adding a `config_base` preset to a repository with no base file counts as
replacing the base (the absent base is an empty layer), even when the managed
overlay is unchanged. Only a pristine first install, with neither file
present, is exempt: there is no current configuration to relax, so it
inherits what the preset declares. These rules apply to repositories with an
explicit entry and to glob-covered repositories.

Install checks the overlay and the base together, before any manifest or forge
write. When the overlay needs adoption and the base needs action, install
reports both problems. A repository whose only problem is adoption is skipped,
as described in [Adopting an existing file](#adopting-an-existing-file). Any
other problem stops the whole run before any write. Convergence runs the same
checks again: a fresh install that adoption or the safety gate blocks stops
before any variable, secret, scaffold file, or PR/MR write, in a dry run and a
live run alike.

#### Read-only manifests

A manifest is read-only when it is an HTTPS URL, a pipe or FIFO such as
`<(cmd)`, or a local file without write permission or on a read-only file
system. When install needs to write to a read-only manifest, for example to
record the [first-install tracker default](#defaults-on-first-install), it
fails before any write and prints the exact entry to add to the manifest
source. This includes a glob-covered repository that needs its own entry.
`--dry-run`, a live install, and `repos status` agree.

Trace signing keys follow their own rule on GitHub. From a read-only manifest,
a fresh install without `--rotate-trace-signing-key` goes ahead without keys
and says so. Any other repository that must record a key fails, and
`--rotate-trace-signing-key` stops the run before it writes a key (see
[Trace signing keys](../../cli/repos.md#trace-signing-keys)).

```bash
fullsend repos status -f repos.yaml --json   # whole-file managed drift
fullsend repos install -f repos.yaml --dry-run
fullsend repos install -f repos.yaml         # rewrite the managed file
```

### Editing the manifest from the command line

`fullsend repos set <path> <value>` edits any field in `repos.yaml`
without opening an editor. The path names the field with dot-separated
keys, and a quoted selector picks a repository entry by its exact name.
Every edit is checked against the manifest schema and validated as a
whole before it is written, so a rejected edit leaves the file
unchanged. Comments and unrelated content are kept.

```bash
# Set a value at defaults, forge, or repository scope
fullsend repos set defaults.config.runtime pi
fullsend repos set gitlab.agent_runner_tags '[fullsend-agent]'
fullsend repos set 'github.repos["acme/api"].config.runtime' claude

# Add a repository entry (or replace an existing one)
fullsend repos set 'github.repos["acme/new-service"]' '{"inference": {"auth": "vertex-wif"}}'

# Delete a key
fullsend repos set --unset 'github.repos["acme/api"].config.runtime'
```

Setting a field under a repository entry that does not exist fails, so a
typo cannot enroll a new repository; set the whole entry to add one. An
empty value never deletes a key: use `--unset`. See
[`repos set`](../../cli/repos.md#repos-set) for the path syntax, value
types, and migration from the removed `repos set-default`.

### Manifest paths and URLs

The `-f`/`--manifest` flag accepts either a local file path or an HTTPS URL.
Most `repos` subcommands support this — see the
[CLI reference](../../cli/repos.md) for details. Remote manifests are fetched
with a 30-second timeout. A manifest, local or remote, can be at most 1 MB.

```bash
fullsend repos status -f https://example.com/manifests/repos.yaml
```

### Concurrency

Most `repos` subcommands accept a `--concurrency` flag to control the
number of parallel API calls or operations. Defaults vary by command
(typically 4 for write operations, 8 for read-only operations). See the
[CLI reference](../../cli/repos.md) for per-command defaults and limits.

### Installing and converging repos

Install and converge repos defined in the manifest:

```bash
fullsend repos install -f repos.yaml
```

Install runs in two phases:

1. **Selection** — install picks the repositories to converge from the
   entries declared in the manifest. Positional arguments filter them and
   never add entries; a selector that matches no declared entry fails
   before any write. See
   [Selecting repositories](../../cli/repos.md#selecting-repositories).
2. **Convergence** — every repo flows through a single probe → diff →
   apply pipeline. Repos whose shim workflow is not yet on the default
   branch are treated as new and fully provisioned (scaffold files,
   variables, secrets) onto the initialization branch. That includes a
   re-run while the initialization PR/MR is still open: variables and
   secrets may already exist from the first run, but the installer
   still updates the same initialization PR/MR rather than opening a
   separate upgrade PR. Repos whose workflow is already on the default
   branch are checked for component drift (workflow, thin callers,
   variables, secrets, pipeline schedules, GitLab poller protected-ref
   pipeline access, GitLab pipeline-variable override-role inspection —
   typed dispatch activation fails closed without `no_one_allowed`;
   runnable typed templates require that setting to be verified before
   delivery, even when enforcement is requested; legacy variable-based
   wrappers are refused), scaffold content drift, managed `.fullsend/config.yaml`
   drift, scaffold ref drift, and declared configuration-preset drift against
   `.fullsend/config.base.yaml`. When a GitHub repo resolves `vendor: false`,
   stale assets from an earlier vendored install are reported as
   `vendored-assets` drift and removed (see
   [Turning vendoring off](../../cli/repos.md#turning-vendoring-off)). Missing
   or drifted components are repaired
   automatically; ref updates are committed as PRs (or direct pushes with
   `--direct`).

> **GitHub prerequisite:** GCP WIF provisioning
> (`fullsend inference provision`) must be completed before running install.
> For self-managed mints, also run `fullsend mint enroll`. The hosted
> community mint needs no enrollment. For GitLab prerequisites and inference
> setup, see [Configuring GitLab](configuring-gitlab.md#prerequisites).

> **Note:** When your token does not have direct push access to a target
> repository, the install command creates a fork and submits the scaffold
> PR from the fork. To avoid fork-based delivery, ensure you have write
> (or admin) access to the target repositories before running install.
> GitHub App installation tokens never fork: they push a branch and open the
> scaffold PR in the target repository. Install the App on each target
> repository with Contents, Pull requests and Workflows read and write
> permissions. See
> [Scaffold PR delivery](../../cli/repos.md#scaffold-pr-delivery).

Preview what would change without making modifications:

```bash
fullsend repos install -f repos.yaml --dry-run
```

Glob patterns are supported:

```bash
fullsend repos install "acme/*" --direct --concurrency 8
```

A filtered install requires credentials only for the selected
repositories' forges. With both GitHub and GitLab entries in the
manifest, `fullsend repos install gallen/integration-service` needs
`GITLAB_TOKEN` but not `GH_TOKEN`. Installing the whole manifest still
requires credentials for every forge that has repos.

Install a subset of agent roles (defaults to
`triage,coder,review,fix,retro,prioritize`) by declaring `config.roles`:

```bash
fullsend repos set defaults.config.roles '[triage, coder, review]'
fullsend repos install -f repos.yaml
```

For repositories that enforce DCO, set `signoff: true` under `defaults`,
a forge section, or a repo entry. Install then adds a `Signed-off-by`
trailer for the authenticated installing user to every scaffold commit,
on GitHub and GitLab, whether delivered directly or by PR/MR. A repo entry
overrides its forge section, which overrides `defaults`, and an explicit
`false` disables an inherited `true`. Install fails before committing
anything if the token's user identity cannot be resolved. See
[DCO sign-off](../../cli/repos.md#dco-sign-off).

```bash
fullsend repos set defaults.signoff true
fullsend repos install -f repos.yaml
```

Scaffold commits are also [signed](../../glossary.md#signed-commit),
independently of the DCO trailer. On GitHub the install asks GitHub to
create each commit on your behalf, so GitHub signs it, it shows as
**Verified**, and it passes rulesets that require signed commits —
whichever token type you use. The exception is a vendored install
(`vendor: true`): its binary is uploaded through an API that GitHub does
not sign for user tokens, so it cannot target a repository that requires
signed commits. On GitLab the commit is signed only where web-based
commit signing is set up: the group or project setting on GitLab.com,
the instance's signing key on Self-Managed and Dedicated. See
[Signed commits](../../cli/repos.md#signed-commits) for details.

#### GitLab trigger-only service account

For GitLab repos, install also converges a dedicated trigger-only service
account that owns a push-capable deploy key on the default branch's
exact-name protected-branch rule and a pipeline trigger token. It uses only
project-scoped authority, so it works on GitLab.com Free. Once the trigger
dispatch activation is recorded, the managed webhook posts to its published
trigger token instead of the Poller-owned one. The managed polling schedules
move to that account, so a scheduled poll runs as it, only when the account
also has merge permission on the protected default branch. Otherwise the
schedules stay installer-owned on the Poller route while the webhook route is
active. Details are in
[Configuring GitLab](configuring-gitlab.md) and the
[`repos install` reference](../../cli/repos.md).

- **Install and recovery:** re-run `repos install`. Every install first runs a
  containment-only recovery that revokes an unfinished bootstrap token and
  restores Developer access for the account, before the configuration
  preflights and every later stage. The same early pass removes an already
  published token variable and revokes the owned triggers across the current
  and retired generations when the override role is weakened or unreadable or
  the variable is unsafe, even when a later install stage fails. Deletion and
  revocation are attempted independently. A repo whose containment fails is reported
  as failed and takes no scaffold, configuration, or role change in that run.
  A glob that cannot be expanded does not stop containment of explicitly
  listed repos or of repos another glob resolved.
- **Deferral:** install defers, and polling stays the dispatch path, when the
  default branch has no exact-name protection rule, when project service
  accounts are unavailable, when the pipeline-variable override role is not
  `no_one_allowed`, or when the recorded branch differs from the
  default branch. A deferral is not a failure. Before it returns, an unsafe
  published token variable is removed and its trigger revoked. A deferral also
  hands any managed polling schedule the account owns back to the installer.
- **Rotation:** `fullsend repos install --rotate-gitlab-trigger-account`
  replaces the account, deploy key, push entry, and trigger token with a fresh
  generation. The previous account is first confirmed at Developer access, and
  rotation stops without replacing anything when that cannot be verified. When
  the new account's activation is recorded in the same run, the previous
  generation's managed polling schedules move straight to the new account.
  Otherwise they are handed back to the installer and move to the new account
  once its activation is recorded.
- **Uninstall:** `repos uninstall` deletes the managed polling schedules the
  account owns, along with the account.
- **Breaking change:** the ledger now records `trigger_token_digest` (the
  trigger token digest) and `publication_pending` (the replacement-publication
  marker). Older strict CLIs reject either key before they read the ledger
  version, so they cannot decode it and fail `repos install`, `repos status`,
  and `repos uninstall` regardless of schedule ownership. Once the account
  owns the managed polling schedules, older CLIs also treat them as foreign
  and refuse cleanup. Upgrade the CLI first and do not delete the schedules by hand; see the
  [CLI reference](../../cli/repos.md#trigger-only-service-account).
- **Branch change:** run `repos uninstall`, then `repos install`.

## Day-2 operations

### Checking installation status

Compare the manifest against actual repo state:

```bash
fullsend repos status -f repos.yaml
```

Filter to specific repos:

```bash
fullsend repos status --repo acme/api --repo acme/web
```

The command reports per-repo status (installed, not installed, error) and
any configuration drift. Returns a non-zero exit code when drift or
errors exist, making it suitable for CI checks.

Inference credentials are checked against each repo's own
`inference.auth`. A missing credential of the selected method and a
leftover fullsend-managed secret of the other method are both reported as
drift, by name only. See
[`repos status` output](../../cli/repos.md#output).

For GitLab repos, status also reports the trigger-only service account as
`not-installed`, `ready`, `drifted`, `deferred`, `in-progress`,
`cleanup-pending`, or `unreadable`, with the reason and resource IDs and never
a token value. Every state except `not-installed`, `deferred`, and `ready` is
drift. The managed `FULLSEND_TRIGGER_TOKEN` and `FULLSEND_WEBHOOK_SECRET`
variables and the reserved `FULLSEND_TRACE_SIGNING_KEY` are not reported as
orphans; any other unrecognised `FULLSEND_` variable still is.

For repos with a managed `.fullsend/config.yaml` and an explicitly pinned
`fullsend_ref`, status also checks the installed file against the
configuration schema that ref reads. It reports
`config compatibility (fullsend_ref <ref>)` as `incompatible` when the ref
cannot read the installed file, or as `unknown` (and the file as drift
rather than current) when the schema cannot be determined. `--json` output
carries `config_compatibility`, `config_compatibility_reason` and
`config_schema_ref`. See [`repos status` output](../../cli/repos.md#output).

For GitLab repos, status also reports the webhook fast path as a
`gitlab_webhook` object with a `state`, the booleans `webhook_present`,
`webhook_configured`, `trigger_token_ready` and `webhook_secret_ready`, and the
reasons, never a token, secret or webhook URL. The states are `ready`,
`missing` (no managed webhook yet), `drifted` (misconfigured, or an unmet safety
or readiness contract), `deferred` (nothing provisioned and the pipeline
wrapper does not include the dispatcher yet), and `unreadable`. Every state
except `ready` and `deferred` is `gitlab-webhook` drift, so it makes the command
exit non-zero. See [`repos status` output](../../cli/repos.md#output) for what
each state means. Once the scaffold is merged to a protected default branch and
the poller is deployed, the poller is the dispatch backstop, so a fast path that
is not `ready` delays dispatch by up to one poll interval instead of stopping
it. A fresh install whose scaffold merge request is still open, or whose default
branch is unprotected, dispatches nothing on either path until then.

For installed GitLab repos, status also scans the default-branch CI
configuration for unrelated CI work across trigger, agent child
(`source=pipeline`) and scheduled (`source=schedule`) pipelines, whatever the
trigger account state. Results appear as a warning section in table output and
as `gitlab_unrelated_ci_work` in `--json` output, and they add no drift. See
[`repos status` output](../../cli/repos.md#output).

Use `--json` for machine-readable output:

```bash
fullsend repos status -f repos.yaml --json
```

### Detecting and reconciling configuration drift

Run `repos install` to detect and fix component drift (workflow, thin
callers, variables, secrets, pipeline schedules, GitLab poller
protected-ref pipeline access, the GitLab webhook fast path — a pipeline
trigger token, webhook secret, and project webhook, deferred until the
dispatcher and the scripts it sources, transitively, are on the default branch, the
default branch is protected, and `no_one_allowed` is verified —
GitLab pipeline-variable override-role
inspection — typed dispatch activation fails closed without
`no_one_allowed` before runnable template delivery, even when enforcement
is requested; legacy variable-based wrappers are refused), scaffold ref drift,
scaffold content drift, declared configuration-preset drift, managed
`.fullsend/config.yaml` drift, and stale vendored assets left by an earlier
vendored install (`vendored-assets`, removed when a GitHub repo resolves
`vendor: false`; see [Turning vendoring off](../../cli/repos.md#turning-vendoring-off))
across all manifest repos:

```bash
fullsend repos install -f repos.yaml
```

Preview what would change without modifying anything:

```bash
fullsend repos install -f repos.yaml --dry-run
```

The convergence phase checks all components (workflow, thin callers,
variables, secrets, pipeline schedules, GitLab poller protected-ref
pipeline access, GitLab webhook fast-path provisioning and repair (the
trigger token, webhook secret, and project webhook are created or repaired
only once the readiness gates are met, otherwise deferred without changes;
a trigger token owned above Developer, or whose owner cannot be verified, is
revoked with its webhook and the fast path stays disabled — install-time
Maintainer access is never kept as a runtime credential; hooks fullsend
does not own are left untouched) — a disabled GitLab schedule is
reported as drift and reactivated only when `--reactivate-schedules` is
passed; GitLab pipeline-variable override-role inspection — typed
dispatch activation fails closed without `no_one_allowed`;
runnable typed templates require the setting to be verified before delivery,
even when enforcement is requested; legacy variable-based wrappers are refused), scaffold content drift (including structural rewrites of
`.gitlab/ci/fullsend-pipeline.yml` at an unchanged template ref),
declared configuration-preset drift, managed `.fullsend/config.yaml` drift,
stale vendored assets (`vendored-assets` drift when a GitHub repo resolves
`vendor: false`; see [Turning vendoring off](../../cli/repos.md#turning-vendoring-off)),
and scaffold workflow refs against the manifest. Missing or drifted
components are repaired automatically (disabled pipeline schedules are the
exception — see above); a changed preset replaces only
`.fullsend/config.base.yaml` and leaves the managed configuration intact. A
drifted managed configuration file is rewritten wholesale, unless the
existing file predates managed-configuration adoption (missing the
ownership marker) — that case is reported as adoption required and left
untouched instead of rewritten, and it stops that repository's install or
convergence before any writes until the file is adopted; the other
repositories still converge. Ref
updates are committed as PRs (or direct pushes with `--direct`).

Use `repos status` for a read-only drift report (no changes applied). For
GitLab repos, status also reports `protected-ref-pipeline` drift when the
poller loses merge access to create pipelines on the protected default
branch:

```bash
fullsend repos status -f repos.yaml --json
```

### Adding repos

Declare the repo in the manifest, then install it:

```bash
fullsend repos set 'github.repos["acme/new-api"]' '{}'
fullsend repos install acme/new-api --direct
```

The new entry inherits `inference.auth` from its forge section or
`defaults`. When neither selects a method, set it on the entry:

```bash
fullsend repos set 'github.repos["acme/new-api"]' '{"inference": {"auth": "openai-api-key"}}'
fullsend repos install acme/new-api --openai-api-key "$OPENAI_API_KEY"
```

Add multiple repos, then preview them:

```bash
fullsend repos set 'github.repos["acme/new-api"]' '{}'
fullsend repos set 'github.repos["acme/new-web"]' '{}'
fullsend repos install acme/new-api acme/new-web --dry-run
```

To cover every repository in an org, declare a glob entry such as
`fullsend repos set 'github.repos["acme/*"]' '{}'`. Naming a concrete
repository the glob covers (`fullsend repos install acme/new-api`)
installs it with the glob's settings. Naming it adds no explicit entry. Any
install, whether it names the repository, passes `"acme/*"` or selects the
whole manifest, gives a glob-covered repository its own entry, copied from the
glob, when it writes that repository's first-install `tracker` default or, on
GitHub, its `trace_context` block
([Trace signing keys](../../cli/repos.md#trace-signing-keys)) to `repos.yaml`.
Edits to the glob entry apply only to repositories that have no entry of their
own.

Per-repo settings live on the entry, never in install flags:
`fullsend_ref`, `mint_url`, `app_set`, `inference.auth`, `vendor`,
`signoff`, `config_base`, and every per-repo config field under `config`
(for example `config.roles`, `config.runtime`, or
`config.inference.region`). For example:

```bash
fullsend repos set 'github.repos["acme/new-api"].config.roles' '[triage, coder, review]'
fullsend repos set 'github.repos["acme/new-api"].fullsend_ref' v2.5.0
```

See [Managed configuration](#managed-configuration) and the
[removed-flag table](../../cli/repos.md#removed-configuration-flags-breaking).

### Removing repos

Remove a repo from the manifest and tear down its installation. File deletions open a PR by default (variables and secrets are deleted immediately via the API). For GitLab repos, uninstall also deletes the `fullsend-poll-state-slash` and `fullsend-poll-state-events` branches, deletes the fullsend-owned webhook fast-path project webhook, revokes its managed Poller-owned pipeline trigger token, and deletes the `FULLSEND_TRIGGER_TOKEN` and `FULLSEND_WEBHOOK_SECRET` variables (on the trigger-account route the account's trigger is revoked by the trigger-account removal below, and `FULLSEND_TRIGGER_TOKEN` was already deleted at install); if that cleanup fails, the scaffold is not removed and the manifest entry is kept for retry. After the webhook fast path is removed and before role cleanup, GitLab uninstall removes the trigger-only service account: the published `FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN` variable, the trigger token, the protected-branch push entry, the deploy key, the managed polling schedules the account owns (deleted without an installer-owned copy), the service account, and finally the `FULLSEND_GITLAB_TRIGGER_ACCOUNT` ledger. When a ledger exists, uninstall requires the dedicated trigger-account removal capability, and it keeps the ledger until cleanup succeeds, so a failed uninstall stops, keeps the manifest entry, and a retry resumes. Generic role-variable cleanup never deletes the ledger or the token variable. Before deleting anything, GitLab uninstall reads the `FULLSEND_GITLAB_ROLE_ROTATION` ownership variable and stops if it is unreadable or malformed. It then revokes role project access tokens and deletes verified fullsend-managed service accounts, leaving administrator-supplied accounts and their credentials in place. The rotation variable is removed last, or kept as an exclusions-only document when it records administrator-supplied service accounts. Pass `--direct` to push file deletions to the default branch:

```bash
fullsend repos uninstall acme/old-api
fullsend repos uninstall acme/old-api --direct
```

When targeting multiple repos (via globs or bulk lists), the command
prompts for confirmation:

```bash
fullsend repos uninstall "acme/*"
```

In non-interactive environments (CI, piped stdin), pass `--yes` to skip
the confirmation prompt:

```bash
fullsend repos uninstall "acme/*" --yes
```

Remove from manifest only (skip teardown — useful when the repo is
already deleted):

```bash
fullsend repos uninstall acme/old-api --manifest-only
```

Tear down without removing from manifest (temporary teardown):

```bash
fullsend repos uninstall acme/old-api --uninstall-only
```

### Rolling out a new fullsend version

To upgrade the scaffold workflow ref across all manifest repos:

1. Update `github.fullsend_ref` (or `gitlab.fullsend_ref` for
   GitLab repos) in `repos.yaml` to the new version.

2. Run install to converge:

   ```bash
   fullsend repos install -f repos.yaml
   ```

3. Review and merge the scaffold PRs in each repo.

Preview what would change without modifying repos:

```bash
fullsend repos install -f repos.yaml --dry-run
```

Push the upgrade directly to the default branch instead of creating a PR:

```bash
fullsend repos install -f repos.yaml --direct
```

Repos that are already SHA-pinned (`@<sha>  # <ref>`) preserve their
pinning style during upgrades when the target ref is a semver tag — the
tag is resolved to a commit SHA and written as `@<sha>  # <ref>`. When
the target ref is a branch name (e.g., `main`), the branch name is
written directly (`@main`) to ensure idempotent convergence. Non-SHA-pinned
repos keep their string ref format (e.g., `@v2.3.0`).
Repos installed with one space before `#` are not rewritten when the
ref and tag are unchanged; they pick up two spaces on the next ref change.

Downgrades are blocked unless `--force` is set. When both the current
and target refs are semver tags, the guard uses version comparison. When
either ref is a SHA (or a mix of SHA and tag), the guard resolves both
to commit SHAs and uses git ancestry checking via the forge API to
detect downgrades. If the ancestry check fails (e.g., API error), the
upgrade proceeds rather than blocking — graceful degradation.

### Required upgrade: shim moves from `pull_request_target` to `pull_request`

GitHub blocks `pull_request_target` by default in public repositories from
December 2, 2026. Repositories whose `.github/workflows/fullsend.yaml` still
triggers on it lose automatic review and retro from that date. Triage,
`/fs-*` commands (including `/fs-review`) and the fix loop keep working.

Upgrade every public repository before that date. Use the route that matches
how the repository was set up:

- **Repositories declared in a manifest:** run `fullsend repos install`,
  using the steps in
  [Rolling out a new fullsend version](#rolling-out-a-new-fullsend-version).
  It rewrites the trigger and the workflow pin in one commit. If it cannot
  fetch the templates of the new version, it upgrades only the pin and prints
  `template fetch at the target ref failed: delivering the pin upgrade only;
  run install again to deliver template changes`. Run it again once the
  fetch works to deliver the trigger.
- **Single repositories set up with `fullsend github setup`** and no manifest:
  upgrade the CLI, re-run `fullsend github setup "$OWNER/$REPO"` with no
  flags, and merge the setup PR it opens. Follow
  [Upgrading a single repository](operations.md#upgrading-a-single-repository):
  the re-run writes repository variables before the PR merges.

Private and internal repositories are not blocked, and a shim that is not
upgraded keeps working there (the warning appears only in public
repositories). Any upgrade to a fullsend version with the new shim installs
the same `pull_request` shim, with the effects below. After the upgrade:

- **Same-repo PRs** are reviewed automatically, as before, except while the
  PR has a merge conflict: GitHub does not run `pull_request` workflows until
  the conflict is resolved. Comment `/fs-review` or `/fs-retro` meanwhile.
- **Fork PRs** are not reviewed or retro'd automatically, whoever the author
  is: an org member or maintainer working from a fork loses the automatic
  review they got before. Someone with triage or higher access (or an OWNERS
  approver or reviewer, when the `owners_file` provider is on) comments
  `/fs-review` on the PR to start a review. Contributors who want automatic
  review push their branch to the repository instead of a fork.
- **Dependabot PRs**: events Dependabot itself triggers (opening the PR,
  its own pushes and closes) are not routed, because GitHub gives those
  `pull_request` runs only Dependabot secrets. A maintainer's push, merge or
  close is routed as usual. Someone with triage access or higher comments
  `/fs-review` on the PR to review it.
- **Private repositories:** before upgrading, check **Settings → Actions →
  General → Fork pull request workflows** and keep "Send write tokens to
  workflows from pull requests" and "Send secrets to workflows from pull
  requests" turned off; `fullsend repos install` does not check them yet
  ([#9630](https://github.com/fullsend-ai/fullsend/issues/9630)). For vendored
  installs (`vendor: true`) it is the only control, because a fork PR runs
  its own copy of the vendored workflows.

Until a repository upgrades, the dispatch still accepts `pull_request_target`
and logs a warning on each such run (see
[Troubleshooting](#shim-still-triggers-on-pull_request_target)).

## Troubleshooting

### Errors that say "server error text withheld"

By default, `fullsend repos` reports a failed credential-bearing API call
without the server's response, for example:

```
GitLab Poller membership reconciliation failed: resolving the GitLab Poller identity: request failed (server error text withheld)
```

Re-run the command with `--verbose` to see the scrubbed HTTP status and
server message, and a stderr log line for each GitLab API request
(method, path and status). This shows which call failed. Known credentials
and token-shaped values are redacted; see
[Verbose output](../../cli/repos.md#verbose-output) for what is shown and
what is redacted.

### Shim still triggers on pull_request_target

The dispatch run shows:

```
This repository's fullsend shim still triggers on pull_request_target, which GitHub blocks by default in public repositories from its announced enforcement date. Re-run 'fullsend repos install' to move the shim to pull_request.
```

The repository runs an old shim. Upgrade it as described in
[Required upgrade: shim moves from `pull_request_target` to `pull_request`](#required-upgrade-shim-moves-from-pull_request_target-to-pull_request).

### Unknown field errors in repos.yaml

The manifest parser strictly validates field names in all sections
(`defaults`, `github`, `gitlab`, `forgejo`, and top-level). A `forgejo:`
section needs a `fullsend` CLI version that includes Forgejo support; an
older binary rejects it as an unknown top-level field.
Unrecognized fields are rejected with an error naming the offending key:

```
parsing manifest YAML: line 8: field fullsend_ref not found in type repos.Defaults
```

Common causes:

- **Typos** — e.g., `mint_ulr` instead of `mint_url`.
- **Deprecated or unsupported fields** — fields that were never part of the
  manifest schema (such as the legacy `mint:` key) are rejected.
- **Wrong nesting level** — e.g., placing `fullsend_ref` under `defaults`
  instead of under `github` or `gitlab`.
- **Renamed fields** — the old flat `config` / `config_hash` *preset*
  keys (a scalar URL or path) were replaced by a nested `config_base`
  object; a scalar `config:` value now fails with "must be a YAML
  mapping" rather than an unknown-field error. Rewrite `config:` as
  `config_base: {source: <value>}` and `config_hash:` as
  `config_base: {sha256: <value>}`, for both `defaults` and per-repo
  entries. A mapping-shaped `config:` block is separate and still
  valid — that is the [managed configuration](#managed-configuration)
  syntax, not the old preset key:

  ```yaml
  # Before
  defaults:
    config: presets/base.yaml
    config_hash: <sha256>

  # After
  defaults:
    config_base:
      source: presets/base.yaml
      sha256: <sha256>
  ```

To fix, correct the field name or remove the unrecognized entry and re-run
the command.

### No inference authentication selected

```
no inference authentication selected for acme/api: set inference.auth (vertex-wif or openai-api-key or openai-wif) on the repository entry, in the github section, or under defaults in repos.yaml (for example: fullsend repos set defaults.inference.auth vertex-wif)
```

The repo resolves no `inference.auth` from its entry, its forge section,
or `defaults`, and its effective configuration declares no complete
`inference.gateway` block. If the message says the gateway is partial
or invalid, complete or fix the block instead. This is expected after
upgrading from a release without the setting. Add the selection at the appropriate level (see
[Inference authentication](#inference-authentication)) and re-run.
`fullsend repos set defaults.inference.auth vertex-wif` restores
the previous Vertex WIF behaviour for every repo. An unrecognised value
(for example `vertex`) fails manifest validation and names the offending
key.

### Partial secret state

Both GCP inference secrets may be absent. If only one exists, `repos status`
reports drift. Supply the project (and, if it cannot be derived, the WIF
provider) with `--vertex-project` / `--vertex-wif-provider` or by declaring
`inference.project` / `inference.wif_provider` in the managed
configuration, so `repos install` rewrites both secrets, or add the
missing one manually; until then a Vertex run fails its early credential
check. An OpenAI run does not use the GCP pair.

### Removed install flags

An install that fails immediately with an error naming a `repos.yaml` key
and a `fullsend repos set` example means the command still passes a
configuration flag that `repos install` no longer accepts, such as
`--roles` or `--vertex-region`. Nothing was read or written. Declare the
value in `repos.yaml` and drop the flag; see the
[removed-flag table](../../cli/repos.md#removed-configuration-flags-breaking)
and [Migrating to manifest-driven `repos install`](migrating-from-github-setup.md).

### Undeclared repository selectors

A positional argument that matches no declared repository fails the whole run
before any write, even when other arguments match, and the error lists each
unmatched selector. This includes a glob such as `other/*` next to a declared
`acme/*`. Add the entry, for example
`fullsend repos set 'github.repos["acme/api"]' '{}'`, then re-run. Dry run and
`repos status` select the same way.

## Tearing down

### Removing individual repos

Remove a repo from the manifest and tear down its fullsend installation.
Scaffold file deletions open a PR by default (repository variables and
secrets are still removed immediately via the API). Pass `--direct` to push
file deletions to the default branch instead:

```bash
fullsend repos uninstall acme/old-api
fullsend repos uninstall acme/old-api --direct
```

Tear down without modifying the manifest (temporary teardown):

```bash
fullsend repos uninstall acme/old-api --uninstall-only
```

When targeting multiple repos, a confirmation prompt appears. In
non-interactive environments (CI, piped stdin), pass `--yes`:

```bash
fullsend repos uninstall "acme/*" --yes
```

### Full teardown

To completely remove fullsend from all manifest repos and GCP
infrastructure, coordinate between roles:

| Step | Role | Command |
|------|------|---------|
| 1 | Platform Admin | `fullsend repos uninstall "org/*" --yes` (forge-side cleanup + manifest removal) |
| 2 | GCP Admin (Inference) | GitHub: `fullsend inference deprovision <org>` (WIF cleanup). GitLab: `inference deprovision` does not cover the shared `gitlab-oidc` provider — see [Operations § Per-repo teardown](operations.md#per-repo-teardown) step 6 to revoke each repo's WIF trust instead. |
| 3 | GCP Admin (Mint) | `fullsend mint unenroll <owner/repo>` for each enrolled repository, adding `--delete-provider` to permanently delete its dedicated WIF provider (self-hosted mints only; not needed for the hosted community mint). Org entries left by the removed per-org enrollment need manual cleanup — see [Cleaning up legacy per-org mint state](../infrastructure/mint-administration.md#cleaning-up-legacy-per-org-mint-state). |

Each `fullsend` command that prompts for confirmation accepts a skip
flag: `--yes` for `repos` commands, `--yolo` for `github` and `mint`
commands.

## See also

- [Configuring GitLab](configuring-gitlab.md) — GitLab-specific getting-started guide
- [Operations](operations.md) — Day-2 per-repo administration and standalone commands
- [CLI Reference: fullsend repos](../../cli/repos.md) — Full flag and subcommand reference
- [Mint administration](../infrastructure/mint-administration.md) — Token mint deployment and management
