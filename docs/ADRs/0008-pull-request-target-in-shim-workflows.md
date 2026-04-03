---
title: "8. Use pull_request_target in shim workflows"
status: Accepted
relates_to:
  - security-threat-model
topics:
  - workflows
  - security
  - pull-request-target
---

# 8. Use pull_request_target in shim workflows

Date: 2026-04-02

## Status

Accepted

## Context

The shim workflow in enrolled repos (`.github/workflows/fullsend.yaml`) references `FULLSEND_DISPATCH_TOKEN` to trigger agent dispatch. Using `pull_request` as the trigger means a malicious PR could modify the workflow file to exfiltrate this token — `pull_request` runs the *PR branch* version of the workflow. Using `pull_request_target` runs the *base branch* version, so PR authors cannot alter the workflow that executes.

## Decision

Use `pull_request_target` for PR-related events in the shim workflow. The shim never checks out PR code — it is a static curl call that forwards event metadata to the dispatch workflow in `.fullsend`.

**Why this is safe despite `pull_request_target`'s reputation:** The "pwn request" vulnerability class requires `pull_request_target` combined with checkout of untrusted code and execution of that code. Our shim does none of that — it reads only `github.event_name`, `github.repository`, and `toJSON(github.event)` from the event context, then curls the dispatch endpoint. No checkout, no build, no script execution from the PR.

**Residual risk:** A compromised dispatch token could trigger `workflow_dispatch` events on `.fullsend`. This is a DoS vector (burn Actions minutes) but not credential theft — the dispatch workflow reads its own repo secrets, and the caller cannot influence which secrets are accessed. This risk is acceptable.

CODEOWNERS on the shim workflow path provides defense-in-depth: even if an attacker could somehow modify the base branch workflow, the change requires human approval.

**Alternatives considered:**

1. **`pull_request`** — exposes the dispatch token to PR-authored workflow modifications. Rejected.
2. **No token / webhook-based dispatch** — requires a hosted webhook receiver, breaking compute-platform agnosticism. Rejected.
3. **Org-level `pull_request_target` prohibition** — some orgs disable `pull_request_target` via repository rulesets. Document as a known configuration requirement for adopters.

## Consequences

- PR authors cannot modify the shim workflow to exfiltrate the dispatch token.
- The shim must never be extended to checkout PR code — this invariant must be maintained as the shim evolves.
- Orgs with blanket `pull_request_target` prohibitions must allowlist the shim workflow.
- Security auditors reviewing the repo will flag `pull_request_target` — the shim's inline comments explain why it is safe.
