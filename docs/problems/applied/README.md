# Applied Problem Considerations

This directory contains organization-specific considerations for applying fullsend to particular downstream consumers. The core problem documents in `docs/problems/` are organization-agnostic. The applied docs here capture how those general problems manifest in specific organizational contexts.

## Current consumers

- **[konflux-ci](konflux-ci/)** — Kubernetes-native CI/CD platform. The original proving ground for fullsend.
- **[red-hat-p-d](red-hat-p-d/)** — Red Hat Product & Delivery organization. Validating agentic SDLC model through    
  Full Send team experiment.

## Adding a new consumer

Create a directory under `applied/` named after the organization or project. Include a `README.md` that covers:

1. Why this organization is an interesting target for autonomous agents
2. Technology landscape and organizational specifics
3. How the general problem areas apply to this specific context
4. Any unique considerations not covered by the general problem docs
