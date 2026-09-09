# Key Design Decisions

- **Autonomy model:** Binary per-repo, with CODEOWNERS enforcing human approval on specific paths
- **Problem structure:** Problem-oriented documents (not ADRs or RFCs) that can evolve independently, with ADRs spun off later when decisions crystallize
- **Threat priority order:** External prompt injection > insider/compromised creds > agent drift > supply chain
- **Code generation is considered a solved problem.** The hard problems are review, intent, governance, and security.
- **Trust derives from repository permissions, not agent identity.** No agent trusts another based on who produced the output.
- **CODEOWNERS files are always human-owned.** Agents cannot modify their own guardrails.
- **The repo is the coordinator.** No coordinator agent — branch protection, CODEOWNERS, and status checks are the coordination layer.
- **First-class catalog is gated.** Custom prototypes graduate into `fullsend-ai/agents` only when they meet catalog-fit, maturity, and cap criteria ([ADR 0111](../ADRs/0111-first-class-agent-promotion.md)).
- **Organization-specific content is cordoned.** Core problem docs are general; applied considerations live in `docs/problems/applied/`.
