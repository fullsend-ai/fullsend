---
title: "145. Keep the credential deny-list out of the runner-secret refusal"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - harness
  - secrets
  - github-actions
---

# 145. Keep the credential deny-list out of the runner-secret refusal

Date: 2026-10-19

Amends: [ADR 0136](0136-runner-secrets-through-one-stored-secret.md)

## Status

Accepted

## Context

[ADR 0136](0136-runner-secrets-through-one-stored-secret.md) refuses some
names as runner-secret bundle keys, and its refusal reads the sandbox
reserved set (`reservedSandboxKeys`). #3373 makes `env.sandbox` also refuse
the credential-class deny-list: proxy, TLS-trust, Git, interpreter, loader
and shell-behavior names.

Those names are legitimate host-side. `pre_script` and `post_script` need
proxy and TLS settings to reach internal networks, and the sandbox child
never sees a runner-secret value. If the deny-list were merged into
`reservedSandboxKeys`, a bundle carrying, for example, `HTTPS_PROXY` would
fail validation and stop every run that uses it.

The refusal also has two gaps. It matched exact case, so a lowercase key
such as `path` passed, although the `env.sandbox` gate matches
case-insensitively. It refused `LD_*` but not the macOS dynamic-linker family
`DYLD_*`, which the plugin-env door already refuses.

## Decision

This ADR settles the runner-secret refusal's name set once `env.sandbox`
refuses the credential deny-list: it does not read the deny-list, it matches
case-insensitively, and it refuses the `DYLD_*` family. The second and third
close gaps in that set rather than open a separate decision.

The `env.sandbox` gate checks the credential deny-list beside
`reservedSandboxKeys`; the deny-list is not merged into that map. The
runner-secret refusal keeps reading `reservedSandboxKeys` and does not
consult the deny-list. It also refuses the OIDC and provider-only keys, the
minted role tokens and workflow-set names refused since ADR 0136, and the
runner-owned prefixes, now including `DYLD_*`. Both doors match
case-insensitively. A credential-class name that is not also in
`reservedSandboxKeys` or under a refused prefix is refused at the
`env.sandbox` door and accepted as a runner-secret key.

## Consequences

- The deny-list can widen without breaking runs whose bundles carry
  host-side settings.
- Both doors read `reservedSandboxKeys`, so a runner-infrastructure name
  added there is refused at both with no second list to keep in sync.
- Proxy, TLS-trust and Git settings stay available to host-side scripts
  through runner secrets.
- ADR 0136's refused names stay refused, now with their case variants and
  the `DYLD_*` family, so a bundle key such as `path` or `github_token` fails
  validation.
