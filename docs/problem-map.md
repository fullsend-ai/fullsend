# Problem Domain Map

A visual map of how the problem domains in this project relate to each other.

## Diagram

```mermaid
graph TD
    classDef security fill:#f9d4d4,stroke:#d32f2f,color:#000
    classDef agents fill:#d4e6f9,stroke:#1565c0,color:#000
    classDef human fill:#d4f9d6,stroke:#2e7d32,color:#000
    classDef foundation fill:#f9f0d4,stroke:#f9a825,color:#000

    STM["Security Threat Model<br/><i>Injection, insider threats,<br/>drift, supply chain</i>"]:::security
    CR["Code Review<br/><i>Specialized sub-agents,<br/>zero-trust review phases</i>"]:::agents
    AA["Agent Architecture<br/><i>Roles, authority boundaries,<br/>coordination model</i>"]:::agents
    AI["Agent Infrastructure<br/><i>Where agents run,<br/>resources & isolation</i>"]:::agents
    IR["Intent Representation<br/><i>Capturing & verifying<br/>authorized intent</i>"]:::human
    GOV["Governance<br/><i>Who controls policies,<br/>permissions, guardrails</i>"]:::human
    AS["Autonomy Spectrum<br/><i>Auto-merge vs. escalate,<br/>per-repo binary model</i>"]:::human
    HF["Human Factors<br/><i>Role shift, review fatigue,<br/>contributor motivation</i>"]:::human
    ARI["Architectural Invariants<br/><i>Constraints that must<br/>always hold true</i>"]:::foundation
    RR["Repo Readiness<br/><i>Coverage, CI maturity,<br/>readiness criteria</i>"]:::foundation
    CC["Codebase Context<br/><i>Layered context model,<br/>CLAUDE.md, org context</i>"]:::foundation
    ACC["Agent-Compatible Code<br/><i>Static typing, error locality,<br/>tooling ecosystems</i>"]:::foundation

    %% Security Threat Model relationships
    STM -->|informs zero-trust| CR
    STM -->|isolation requirements| AI
    STM -->|constrains| AA
    STM -->|drift detection| ARI

    %% Agent Architecture relationships
    AA <-->|two-phase model| CR
    AA <-->|instance topology| AI

    %% Code Review relationships
    CR -->|consumes| IR
    CR -->|needs cross-repo| CC

    %% Intent Representation relationships
    IR <-->|tier escalation| ARI
    IR -->|primary output shifts to| HF

    %% Governance relationships
    GOV -->|controls policies for| STM
    GOV -->|graduation decisions| AS
    GOV -->|who modifies tiers| IR
    GOV -->|exception authority| ARI
    GOV -->|assigns permissions| AA

    %% Autonomy Spectrum relationships
    AS -->|defines agent scope| HF

    %% Foundation relationships
    RR -->|readiness criteria| ACC
    RR -->|context quality| CC
    RR -->|architectural clarity| ARI
    CC -->|structured frontmatter| ARI
    ACC -.->|type checking reduces burden| CR
```

## How to read this

The 12 problem domains cluster into four themes:

- **Security** (red) — The security threat model is foundational, informing zero-trust review, agent isolation, and drift detection
- **Agent System** (blue) — Architecture, infrastructure, and code review form the core agent machinery, tightly interconnected
- **Human & Policy** (green) — Governance, autonomy spectrum, intent representation, and human factors address who controls what and how humans interact with the system
- **Foundation** (yellow) — Repo readiness, codebase context, architectural invariants, and agent-compatible code are prerequisites that enable everything above

### Key structural observations

- **Security Threat Model** and **Governance** are the two most connected nodes — security constrains the technical design, governance constrains the policy design
- **Code Review** is the central integration point where security, architecture, intent, and context all converge
- **Architectural Invariants** bridges intent (what should be true) with readiness (what is true) and security (drift detection)
- The foundation layer flows upward — repos must be ready before agents can operate autonomously
