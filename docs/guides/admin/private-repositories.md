# Enabling fullsend on private repositories

This guide covers what administrators need to configure when enabling fullsend on private repositories. Admin access to the target private repository is required.

## Supported scenarios

The two install modes have different requirements when the target repo is private.

### Per-repo install (recommended for private repos)

Per-repo install is self-contained: the fullsend workflow shim lives inside the target repo under `.fullsend/`, and it calls the public action workflows in the upstream `fullsend-ai/fullsend` repository directly. Because the caller and the called workflow are in the same repo (or the called repo is public), there are no cross-repo visibility constraints. Per-repo install works for any private repo on any GitHub plan.

**This is the recommended install mode when adding fullsend to private repositories.**

### Per-org install (`.fullsend` config repo)

Per-org install uses a shared `.fullsend` config repo in your organization, with enrolled repos calling into it via cross-repo `workflow_call`. GitHub's Actions access controls create a visibility constraint:

| `.fullsend` visibility | Enrolled repo visibility | Result |
|------------------------|--------------------------|--------|
| Public | Any (public, private, internal) | Works |
| Private | Private | Works |
| Private | Public | **Fails** (silent — 0 jobs, no error) |
| Private | Internal (Enterprise Cloud only) | **Fails** (silent) |

The installer creates `.fullsend` as **public by default**, which avoids all visibility edge cases. If your org overrides this to private — for example, to keep orchestration workflows proprietary — then **every enrolled repo must also be private**. Enrolling a public repo into a private `.fullsend` causes silent `workflow_call` failures with no diagnostics.

> **Enterprise Cloud note:** On Enterprise Cloud orgs, GitHub distinguishes between _internal_ and _private_ repo visibility. A private `.fullsend` config repo accepts `workflow_call` only from other _private_ repos — internal repos are excluded. If your org has internal repos you want to enroll, the `.fullsend` config repo must be public or internal (not private).

## How private repos differ from public repos

Fullsend treats all repositories the same at the infrastructure level — the same agents, harness, and pipeline run regardless of visibility. The differences are operational, not architectural:

1. **Information disclosure risk.** Agents process repository content (code, issues, PR descriptions) and produce output (comments, commits, filed issues) that may reference that content. In a public repo this is harmless — the content is already public. In a private repo, agent output that crosses a visibility boundary (e.g., an issue filed in a public `.fullsend` config repo) can leak private information. See [#1189](https://github.com/fullsend-ai/fullsend/issues/1189) for a concrete example involving the retro agent.

2. **Sensitive data exposure.** Private repos are more likely to contain credentials, PII, internal hostnames, or proprietary logic. Agents may reproduce this content in their output — not because they are instructed to, but because quoting context is a natural part of code review, triage summaries, and retrospective analysis. The [security threat model](../../problems/security-threat-model.md#indirect-information-disclosure) documents how indirect disclosure bypasses content-level guardrails.

3. **`.fullsend` config repo Actions logs (per-org install only).** If your org uses per-org install, agent workflows in `.fullsend` may log run artifacts that reference private repo content. Consider whether your `.fullsend` repo's Actions log visibility needs to be restricted, particularly if `.fullsend` is public but enrolled repos are private.

## Which agents are safe to enable by default

All agents are designed to operate safely, but some produce output with higher disclosure risk when processing private repos:

| Agent | Default risk | Notes |
|-------|-------------|-------|
| **triage** | Low | Output stays within the source repo (labels, comments on the triggering issue). No cross-repo disclosure. |
| **coder** | Low | Commits and PR descriptions stay within the source repo. |
| **review** | Low | Review comments stay within the source repo's PR. |
| **retro** | **Higher** | Files improvement issues that may target a different repo. If the target repo has broader visibility than the source, private content can leak. See [#1189](https://github.com/fullsend-ai/fullsend/issues/1189). |
| **prioritize** | **Higher** | Analyzes issues across repos and may produce cross-repo summaries. If summaries reference private repo content, the same disclosure risk applies. |

> **Recommendation:** Start with **triage**, **coder**, and **review** on private repos. Enable **retro** and **prioritize** only after configuring the guardrails described below.

To limit which agents run, edit the `roles` list in `config.yaml`:

- **Per-repo install:** `.fullsend/config.yaml` in the target repo. List only the roles you want enabled:
  ```yaml
  version: "1"
  roles: [triage, coder, review]   # retro and prioritize omitted
  ```

- **Per-org install:** `.fullsend` config repo's `config.yaml`. Use `defaults.roles` for an org-wide default, or add a per-repo override under `repos`:
  ```yaml
  defaults:
    roles: [fullsend, triage, coder, review, retro, prioritize]
  repos:
    my-private-repo:
      enabled: true
      roles: [triage, coder, review]   # retro and prioritize disabled for this repo
  ```

Roles omitted from the list are not dispatched — the dispatcher blocks them before any agent runs.

## Configuring AGENTS.md for private repos

The `AGENTS.md` file in a repository provides instructions that all agents follow. For private repos, add guidance that prevents agents from reproducing sensitive content in their output.

### Example: preventing sensitive content reproduction

Add the following section to your private repo's `AGENTS.md`:

```markdown
## Private repository rules

This is a private repository. When producing output (comments, issues,
commit messages, PR descriptions), follow these rules:

1. **Do not quote credentials, tokens, API keys, or secrets** — even if
   they appear in the code you are analyzing. Reference them by variable
   name (e.g., "the `DB_PASSWORD` environment variable") rather than by
   value.

2. **Do not reproduce internal hostnames, IP addresses, or service
   URLs** in output that may be visible outside this repository. Use
   placeholders (e.g., `internal-service.example.com`) when describing
   architecture.

3. **Summarize rather than quote** when referencing code that contains
   proprietary logic. Describe what the code does, not what it says.

4. **Do not include file paths that reveal internal project structure**
   in issues filed outside this repository. Describe the component
   abstractly (e.g., "the authentication module") rather than by path.
```

### Example: restricting retro agent output

If the retro agent is enabled, add specific guidance for cross-visibility filing:

```markdown
## Retro agent: cross-repo filing

When filing improvement issues based on work in this repository:

1. **Abstract all findings.** Describe patterns and improvements
   generically. Do not include file paths, function names, variable
   names, or code snippets from this repository.

2. **Do not reference internal infrastructure** — hostnames, service
   names, deployment targets, or configuration values must not appear
   in issues filed to public repositories.

3. **Frame improvements as general patterns.** Instead of "the
   `AuthService.validateToken()` method in `internal/auth/` should
   use constant-time comparison," write "token validation should use
   constant-time comparison to prevent timing attacks."
```

## Testing that guardrails are working

After configuring `AGENTS.md` and enabling agents on a private repo, verify that the guardrails prevent information disclosure.

### 1. Create a test issue with sensitive context

File an issue in your private repo that references internal details:

```markdown
## Bug report

The endpoint at `https://api.internal.example.com:8443/v2/auth`
returns a 500 when called with the service account
`svc-pipeline@project.iam.gserviceaccount.com`. The error log
shows the database connection string includes the password in
cleartext.
```

### 2. Verify triage output

After the triage agent processes the issue, review its comment:

- Does it quote the internal hostname verbatim, or describe it abstractly?
- Does it reproduce the service account identifier?
- Does the triage summary avoid including the database connection detail?

### 3. Test cross-repo agents (if enabled)

If the retro agent is enabled, trigger it on a merged PR and check the filed issue:

- Is the issue filed in a repo with broader visibility?
- Does the issue body contain file paths, function names, or code from the private repo?
- Are findings described as general patterns rather than repo-specific details?

### 4. Review Actions logs

Check the GitHub Actions logs for agent runs in both the target repo and `.fullsend`:

- Do log outputs contain sensitive values from the private repo?
- Are the harness-level [secret redaction](../user/customizing-agents.md#harness-yaml-structure) and output scanning working as expected?

> **Note:** Agent output goes through the harness-level `SecretRedactor` pipeline before being applied (see [ADR 0022](../../ADRs/0022-harness-level-output-schema-enforcement.md)). This catches known secret patterns but cannot catch all forms of sensitive content — `AGENTS.md` instructions are your primary defense for context-specific information.

## What should not be deployed based on data sensitivity

Not all private repos are equal. A repo containing open-source code that happens to be private (e.g., pre-release) has different risk than a repo containing PII, financial data, or security credentials.

### High-sensitivity repos (PII, credentials, financial data)

- **Enable only:** triage, coder, review
- **Disable:** retro, prioritize (any agent that produces cross-repo output)
- **Require:** `AGENTS.md` with the private repository rules above
- **Consider:** Restricting the `.fullsend` config repo Actions log visibility, since workflow logs may contain references to private repo content

### Medium-sensitivity repos (proprietary code, internal tooling)

- **Enable:** triage, coder, review
- **Enable with caution:** retro, prioritize — only if AGENTS.md guardrails are configured and tested
- **Require:** `AGENTS.md` with at minimum the sensitive-content-reproduction rules

### Low-sensitivity repos (pre-release public code, internal docs)

- **Enable:** all agents
- **Recommended:** `AGENTS.md` with basic private-repo rules as defense in depth

## See also

- [Installation guide](installation.md) — Initial fullsend setup
- [Customizing agents](../user/customizing-agents.md) — Harness configuration and layered overrides
- [Security threat model](../../problems/security-threat-model.md) — Threat priority and defense considerations
- [#1189](https://github.com/fullsend-ai/fullsend/issues/1189) — Retro agent private content leak risk
