---
title: "114. GitHub Packages access through a host-bound provider fed by the workflow token"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - security
  - sandbox
  - credentials
---

# 114. GitHub Packages access through a host-bound provider fed by the workflow token

Date: 2026-09-15

## Status

Accepted

## Context

Code and fix agents install dependencies inside the sandbox. A public package
on GitHub Packages that another organization owns cannot be read with the
minted App token, even after the mint grants `packages: read`: GitHub scopes
installation tokens to the packages of the org the App is installed in and
answers `403 Permission installation not allowed to Read organization package`
(measured 2026-09-09, fullsend#6649). Anonymous requests get 401 even for public
packages. The job's own Actions token with `packages: read` succeeds, and the
tarball is then served from `pkg-npm.githubusercontent.com` through a
pre-signed URL that expires in minutes and carries no bearer credential.

After the mint the only credential in the runner process was the App token, and
the reusable workflows forward no user secret to the code job, so a repo-level
provider had nothing usable to bind. Sandboxed agents receive credentials only
through OpenShell providers and L7 egress policy
([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md),
[ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md),
[ADR 0065](0065-provider-backed-policy-composition.md)); inference credentials
already follow the run-scoped provider pattern
([ADR 0092](0092-openai-wif-credential-delivery.md)).

## Options

- **Rely on the minted App token.** Same-org only, where `${GH_TOKEN}` already
  serves.
- **Export the Actions token into the sandbox environment.** Replayable at any
  allowlisted host and lands in transcripts. Rejected.
- **A user-supplied PAT.** No secret passthrough exists, and it would add a
  long-lived credential where the job already holds a short-lived one. Rejected.
- **Apply `env.runner` to the runner process before provider creation.** Mint
  runs before harness expansion, so `${GH_TOKEN}` there is already the App
  token and the pre-mint value still needs its own name; harness YAML would
  also gain the power to replace the runner's own credentials, which #5832
  deliberately prevents; and it presumes a user secret the reusable workflows
  do not pass. Rejected.

## Decision

**The Actions workflow token reaches the sandbox only as an OpenShell provider
credential bound to the GitHub Packages hosts; forge identity stays the minted
App token.**

1. On GitHub Actions, the mint step copies the pre-mint `GH_TOKEN` to
   `GH_WORKFLOW_TOKEN` before replacing `GH_TOKEN` with the minted token,
   masks it, and unsets it at cleanup. Outside Actions nothing is copied: a local
   PAT never becomes a workflow token, and a caller-set value is left alone.
2. The variable is a new credential class, provider-only: every harness `${}`
   site (`runner_env`, `env.runner`, `env.sandbox`, `host_files`,
   `validation_loop`) refuses it, pre/post/validation child environments strip
   it, `env.sandbox` cannot name it, and redaction knows its value. Only
   provider credential values read it (never provider config, which is passed
   on argv); the sandbox environment holds the
   placeholder, resolved by the proxy solely at the bound hosts.
3. A repo opts in with a provider (`type: fullsend-github-packages`) whose
   profile binds the placeholder to `npm.pkg.github.com:443` and
   `pkg-npm.githubusercontent.com:443` (the tarball CDN, which ignores it and
   serves pre-signed URLs), both read-only and enforced. The proxy rejects the
   placeholder at any other host (`credential_endpoint_mismatch`), so
   `api.github.com` and `github.com` keep resolving the App token, and the
   post-script keeps pushing with `PUSH_TOKEN`.
4. Nothing ships by default. The provider, profile and `~/.npmrc` line live in
   the repository's `.fullsend`, read from the trusted ref.

## Consequences

- Cross-org installs of public GitHub Packages work with the repository's own
  token and no new identity or secret; private packages of another org stay
  out of reach, as they are for the workflow token itself.
- Placeholder *resolution* is host-bound and read-only: the proxy resolves it
  only at the two registry hosts and rejects it anywhere else
  (`credential_endpoint_mismatch`), and forge writes still go through the
  post-script with the App token. This says nothing about the forge hosts
  themselves — `coder` (which runs both the code and fix stages) uses the
  `fullsend-github.yaml` profile, which is read-write on `api.github.com` and
  `github.com`, as it must be for git/gh operations; the placeholder simply
  cannot be used there.
- The value is readable by the agent: OpenShell resolves a static placeholder in
  the header, path or query of a request to a bound host, and the registry
  echoes unknown package names in 404 bodies (verified on OpenShell 0.0.116).
  This is the free-text-endpoint exposure [ADR 0025](0025-provider-credential-delivery-for-sandboxed-agents.md)
  accepts for every static credential. A recovered literal is the job's own
  `GITHUB_TOKEN` (`contents:write`, `issues:write`, `pull-requests:write`,
  `actions:write`, `packages:read` per the reusable code/fix workflows), not
  the host-bound placeholder — it is not host-bound if replayed as a raw
  `Authorization` header, and on `coder`'s read-write `fullsend-github.yaml`
  profile it can call write-capable GitHub APIs, including `actions:write`,
  which `coder`'s own minted App token does not have. Header-only placement
  is an OpenShell roadmap item, tracked as follow-on.
- GitLab and local runs are unchanged, because nothing is preserved outside
  Actions.
- Provider definitions read from the trusted ref may now reference one more
  runner credential; repos that ship other providers should review their `${}`
  uses.
- Shipping the provider by default and other registries such as `ghcr.io` are
  follow-on decisions.
