# Cross-Org Issue Filing Design

Addresses [#672](https://github.com/fullsend-ai/fullsend/issues/672). Related: [#978](https://github.com/fullsend-ai/fullsend/issues/978) (target_repo restriction).

## Problem

The retro agent's post-script can target `fullsend-ai/fullsend` for upstream improvements, but the token mint is org-scoped. Cross-org proposals fail at the GitHub API level, crash the post-script (`set -euo pipefail`), and lose any remaining proposals. There is no authorization model for cross-org issue filing.

This is the primary mechanism for end-user orgs to send improvement proposals back upstream.

## Design

### Config Keys

Two new keys in `config.yaml`, checked independently:

**Downstream** (the org/repo that sends proposals):

```yaml
send_issues_to:
  - fullsend-ai/fullsend
  - other-upstream/some-repo
```

**Upstream** (the org/repo that receives proposals):

```yaml
accept_issues_from:
  - konflux-ci
  - openkaiden
  - guacsec
```

`accept_issues_from` values are org names. Any enrolled repo in a listed org can send proposals to any repo in the upstream org.

Both keys follow the existing layering convention for config overrides. No special precedence rules.

### Config Lookup Order

When the mint or post-script needs to read config, it checks:

1. `{repo}/.fullsend/config.yaml` (per-repo config)
2. `{org}/.fullsend/config.yaml` (per-org config)
3. If neither exists, treat the key as absent.

This applies to both sides: the mint reading `accept_issues_from` from the upstream, and the post-script reading `send_issues_to` from the downstream.

### Authorization Model

Bidirectional. Both sides must agree:

- **Downstream** declares `send_issues_to: [upstream-org/repo]` in its config.
- **Upstream** declares `accept_issues_from: [downstream-org]` in its config.
- If either side is missing, the cross-org proposal is not filed.

Enforcement is split across two boundaries:

| Check | Where | What |
|-------|-------|------|
| Upstream authorization | Mint | Reads upstream's `accept_issues_from`. If the requesting org is listed, mints a `cross-org-propose` token. If not, refuses. |
| Downstream authorization | Post-script | Reads downstream's `send_issues_to`. If the `target_repo` is listed, uses the cross-org token. If not, skips with a warning. |

The mint does not check the downstream's `send_issues_to`. The post-script does not make token-level security decisions.

### New Mint Role: `cross-org-propose`

Permissions: `issues: write` only.

This is the only role that causes the mint to read config from a GitHub repo. All other roles remain stateless OIDC validation.

When the mint receives a `cross-org-propose` request:

1. Validate OIDC claims as usual (issuer, audience, `job_workflow_ref`, `repository_owner` in `ALLOWED_ORGS`).
2. Identify the requesting org from OIDC claims.
3. Use the App JWT to read the target org's config (lookup order above) for `accept_issues_from`.
4. If the requesting org is in the list, mint an installation token scoped to `issues: write` on the target repo.
5. If not, return a clear refusal (not an error — the workflow handles it gracefully).

### Workflow Changes

The retro reusable workflow (`reusable-retro.yml`) gets new steps between the existing `mint-token` step and the agent run:

1. **Read downstream config** — extract `send_issues_to` from the appropriate `config.yaml`.
2. **For each cross-org target**, make a `mint-token` call with `role: cross-org-propose` and the target repo. The mint checks `accept_issues_from` on the upstream side. If authorized, returns a scoped token. If not, logs a notice and moves on.
3. **Write tokens** to `$RUNNER_TEMP/crossorg-tokens/`, one file per target (filename: `{owner}--{repo}`). Set `CROSSORG_TOKENS_DIR` env var.

The primary org token flow is unchanged. If `send_issues_to` is empty or absent, no extra mint calls happen.

### Post-Script Changes

`post-retro.sh` changes:

1. **Read `send_issues_to`** from config (lookup order above).
2. **For each proposal**, before filing:
   - Same-org target: use `GH_TOKEN` as today.
   - Cross-org target in `send_issues_to` with a token in `$CROSSORG_TOKENS_DIR`: use that token.
   - Cross-org target not in `send_issues_to`: log a warning, skip.
   - Cross-org target in `send_issues_to` but no token file (mint refused): log a warning, skip.
3. **No crash on skipped proposals.** Continue processing remaining proposals.

### Agent Prompt Changes

The retro agent definition (`agents/retro.md`) and skill (`skills/retro-analysis/SKILL.md`) keep their hardcoded `fullsend-ai/fullsend` upstream guidance. The only change is removing the TODO(#833) warnings that discourage cross-org filing, since the authorization mechanism now exists.

### Generality

The `cross-org-propose` role, `send_issues_to`, and `accept_issues_from` config keys are agent-agnostic. The retro agent uses them first, but any future OOTB or custom agent can reuse the same mechanism to file cross-org issues. The retro agent's hardcoded upstream target is specific to retro; the cross-org machinery is not.

This aligns with the downstream/upstream federation model described in `docs/architecture.md`.

### Failure Modes

| Scenario | Behavior |
|----------|----------|
| `send_issues_to` missing from downstream config | No cross-org proposals attempted. Same-org proposals file normally. |
| `accept_issues_from` missing from upstream config | Mint refuses `cross-org-propose`. Post-script skips proposal with warning. |
| Upstream doesn't have the App installed | Mint can't find installation. Returns refusal. Post-script skips. |
| Mixed proposals (some authorized, some not) | Files what it can, skips the rest, no crash. |
| Downstream `send_issues_to` lists a target but mint refused | Post-script finds no token file, logs warning, skips. Likely a config mismatch between orgs. |

## Testing

1. **Mint unit tests** for `cross-org-propose`:
   - Authorized: upstream config lists requesting org, token minted.
   - Unauthorized: upstream config missing or doesn't list requesting org, request denied.
   - Config lookup order: per-repo config found, use it; not found, fall back to org config; neither, deny.
   - App not installed in upstream org, deny with clear error.

2. **Post-script tests**:
   - Cross-org target in `send_issues_to` with valid token, issue filed.
   - Cross-org target not in `send_issues_to`, skipped with warning.
   - Cross-org token missing (mint refused), skipped with warning.
   - Mixed proposals (some in-org, some cross-org, some unauthorized), files what it can, skips the rest.

3. **Integration test** — end-to-end:
   - Downstream org with `send_issues_to: [upstream-org/repo]`.
   - Upstream org with `accept_issues_from: [downstream-org]`.
   - Retro agent produces a cross-org proposal, issue appears in upstream repo.
