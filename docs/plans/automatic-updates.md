# Design Document: Automatic Updates

[ADR 48](../ADRs/0048-automatic-updates.md) decision is to implement a system that
uses a single tag to control all the components' version Fullsend uses. This design
document describes in detail the current state and the desired implementation:

## Current state

Currently there are four versions within Fullsend system:

* Reusable Workflows: jobs use the line
`uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v0`
to pull reusable workflows from Fullsend. This is hard-coded as it can't be templated with
an expression.
* CLI: the `action.yml` YAML in the root of the repository uses
`inputs.version` (defaults to `latest`). This is passed around.
* GH Actions: reusable workflows clone the `fullsend-ai/.fullsend` repository
at it's `inputs.fullsend_ai_ref` (defaults to `v0`) and use the actions with a
relative path: `uses: ./.defaults/.github/actions/validate-enrollment`. This
is passed around.
* OpenShell sandbox images: currently images use the `latest` tag and can't be
templated as harnesses and `fullsend run` do not allow for that. These have no Semver
tags.

When we release, we create a new Semver tag (`vMAJOR.MINOR.PACTH`) and move the `v0` tag
to the new Semver tag. As users have configured `v0` for workflows and actions, and
`latest` for the binary, they get automatically the new changes.

To change versions in repository mode you change your `.github/workflows/fullsend.yaml`.
First the `uses: ... reusable-dispatch.yml@v0` needs to reference your version. Then
the `fullsend_ai_ref` passed should be changed. Finally you add `fullsend_version` to
that job and set it to the proper version.

To change versions in org mode you change the call to the reusable workflows each one of
your workflows on `.fullsend` (`fix.yaml`, `triage.yaml`) do. The changes required are the
same as in repository mode, just in a different file.

## Implementation

With `fullsend_ai_ref` and `fullsend_version` it is easy to control from a single
place which version should be use. A step in the shim would pull the version
from the `config.yaml` and will pass it around. However the reusable workflows can't
benefit from this.

So the version pinning should happen another way. We will introduce a new parameter
called `--upstream-ref` to both `admin install` and `github setup` that accepts
a reference to `fullsend-ai/fullsend`. By default the value is pulled from the
`cli.Version` variable injected at compile time. If any other value is specified
then it is used.

This value (`upstreamRef`) would be used to template the following files:

* `internal/scaffold/fullsend-repo/templates/shim-per-repo.yaml` (it becomes
`.github/workflows/fullsend.yaml` in per-repo mode).
* `internal/scaffold/fullsend-repo/.github/workflows/*.yml` (it becomes
`.github/workflows/*.yml` on per-org mode)

So every call to reusable workflows should be templated (regardless of the install mode).
The template string will be `__FULLSEND_REF__`.

Given that we are changing this code, we may as well update the variable names to reflect
better their real usage:

* `fullsend_ai_ref` -> `fullsend_actions_ref`
* `fullsend_version` -> `fullsend_cli_ref`

So the template looks like (excluding other details):

```yaml
# fullsend.yaml or <stage>.yml
uses: fullsend-ai/fullsend/.../reusable-*.yml@__FULLSEND_REF__
with:
  fullsend_actions_ref: __FULLSEND_REF__
  fullsend_cli_ref: __FULLSEND_REF__
```

Running `fullsend github setup org/repo --upstream-ref latest` the template will be rendered
as (excluding other details):

```yaml
# fullsend.yaml or <stage>.yml
uses: fullsend-ai/fullsend/.../reusable-*.yml@latest
with:
  fullsend_actions_ref: latest
  fullsend_cli_ref: latest
```

Running `fullsend github setup org/repo --upstream-ref main` the template will be rendered
as (excluding other details):

```yaml
# fullsend.yaml or <stage>.yml
uses: fullsend-ai/fullsend/.../reusable-*.yml@main
with:
  fullsend_actions_ref: main
  fullsend_cli_ref: main
```

Running `fullsend github setup org/repo --upstream-ref v0.15.0` the template will be rendered
as (excluding other details):

```yaml
# fullsend.yaml or <stage>.yml
uses: fullsend-ai/fullsend/.../reusable-*.yml@v0.15.0
with:
  fullsend_actions_ref: v0.15.0
  fullsend_cli_ref: v0.15.0
```

## Some Future Problems

* Currently images are not versioned, they just have the `latest` tag. This needs to
change so everything moves at the same pace.
* When (and if) we externalize the default agents, in case those have an independent
version which is likely, then the Fullsend version will need to pin to those versions
at the moment of release.
