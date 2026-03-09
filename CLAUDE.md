# CLAUDE.md

Fullsend is a living design document exploring fully autonomous agentic development for the [konflux-ci](https://github.com/konflux-ci/) GitHub organization. It contains no application code — only prose documents organized by problem domain. See [README.md](README.md) for the full document index.

## How to work in this repo

- This is a design exploration, not a spec. Documents should present multiple options with trade-offs, not prescribe single solutions.
- Each problem document has an "Open questions" section — this is where unresolved issues live.
- When adding new problem areas, create a new file in `docs/problems/` and link it from `README.md`.
- The security threat model (threat priority: external injection > insider > drift > supply chain) should inform all other documents.
- Coverage data in `repo-readiness.md` references the live dashboard at https://konflux-ci.dev/coverage-dashboard/ and may need periodic updates.
- The target audience is the konflux-ci contributor community — keep language accessible, avoid presuming solutions.

## Key design decisions made

- **Autonomy model:** Binary per-repo, with CODEOWNERS enforcing human approval on specific paths
- **Problem structure:** Problem-oriented documents (not ADRs or RFCs) that can evolve independently, with ADRs spun off later when decisions crystallize
- **Threat priority order:** External prompt injection > insider/compromised creds > agent drift > supply chain
- **Scope:** All repos in the konflux-ci org (heterogeneous — Go, React, Tekton, Python, shell)
- **Code generation is considered a solved problem.** The hard problems are review, intent, governance, and security.
- **Trust derives from repository permissions, not agent identity.** No agent trusts another based on who produced the output.
- **CODEOWNERS files are always human-owned.** Agents cannot modify their own guardrails.
- **The repo is the coordinator.** No coordinator agent — branch protection, CODEOWNERS, and status checks are the coordination layer.
- **The konflux-ci/architecture repo** is a key source of architectural invariants that agents should enforce.
