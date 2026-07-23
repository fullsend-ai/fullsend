# Terminology: Tier Conventions

The term "tier" is used in multiple distinct contexts across this codebase. Always use a descriptive prefix to avoid ambiguity:

| Prefix | Meaning | Defined in |
|---|---|---|
| **credential delivery tier** | The four-tier model for how agents receive credentials: (1) prefetch + post-process, (2) providers + L7, (3) host-side REST server, (4) host files | [ADR 0025](../ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md) |
| **intent authorization tier** | The four-tier model for change authorization: (0) standing rules, (1) tactical/issue, (2) strategic, (3) organizational | [intent-representation.md](../problems/intent-representation.md) |
| **configuration tier** | The three-tier inheritance model for agent configuration: upstream defaults -> org config -> per-repo overrides. The `customized/` overlay mechanism (ADR-0035) is deprecated; use config-driven agent registration per [ADR 0064](../ADRs/0064-deprecate-customized-directory-overlay.md) | [ADR 0035](../ADRs/0035-layered-content-resolution.md) |

**Do not** use bare "Tier N" or "tier" without a prefix — the same number means different things in different contexts (e.g., "Tier 2" could be provider-based credential delivery or strategic intent authorization). External tier references (e.g., "GitLab Free tier", "GitHub plan tiers") are exempt from this convention.
