# Agent-Compatible Code

What properties of code affect how effectively agents can generate, review, and refactor it?

## Language selection criteria

Not all languages are equally suited for agentic development. The properties that matter:

**Static type checking** catches type mismatches at compile time rather than at runtime, constraining the solution space so agents get immediate feedback on errors.

**Error locality** means errors point to the source, not somewhere downstream. In dynamically typed languages a type error may not surface until deep in the call stack; in statically typed languages the compiler identifies the exact mismatch.

**Type-propagated refactoring safety** is critical for autonomous changes. When an agent changes a function signature, the compiler surfaces every caller that needs updating — no heuristic search, no reliance on test coverage to catch what was missed.

**Tooling ecosystem** — LSP servers, formatters, and linters provide semantic information and deterministic quality checks that agents can invoke programmatically. Mature tooling reduces guesswork about code structure.

**Deployment simplicity** — single-artifact builds are easier to reason about than systems with runtime dependency graphs. Fewer moving parts mean fewer ways for an agent to introduce subtle breakage.

## In-house vs consumed code

There are two different standards, because the org doesn't control what languages upstream projects choose.

### In-house code

Code owned by the organization — services, controllers, tooling — should meet the language criteria directly. When new tooling is created or experiments graduate to production, prefer languages that satisfy static typing, error locality, refactoring safety, mature tooling, and simple deployment.

This doesn't mean rewriting existing code. It means that when choosing a language for new work, agents benefit from typed languages with strong tooling ecosystems.

### Consumed dependencies

Upstream libraries and services (third-party libraries, external tools, platform SDKs) can't be required to change languages. The requirement shifts to the integration boundary:

**Schema-validated inputs and outputs** — if the org's code calls an external service or library, the contract should be explicit and validated. API schemas, protobuf definitions, OpenAPI specs — anything that makes the boundary machine-checkable.

**Pinned versions and digests** — consumed dependencies should be pinned to specific versions or content hashes. This prevents unexpected changes from propagating into the system without review.

**Integration tests** — the boundary between the org's code and the external dependency needs tests that verify the contract holds. If the upstream library changes its behavior in a way that breaks the integration, the test catches it.

**Narrow contact surface** — minimize the API surface the org's code touches. Don't import an entire library when you only need one function. Wrap external dependencies in adapters that expose only the needed functionality.

When a consumed dependency runs as a container image, the integration boundary is already container-isolated — the dependency's internal implementation language doesn't leak into the rest of the system. The risks are config schema drift and unvalidated outputs. The mitigation is schema-validating the inputs, pinning the image digest, and testing the contract. See [applied docs](applied/) for organization-specific examples.

## Relationship to other problem areas

**Agent drift** — typed code constrains the solution space, reducing the ways an agent can drift over time. Type errors surface immediately rather than accumulating as latent bugs.

**Code review** — a type checker handles entire categories of review concerns (type safety, null pointer dereference in nullable-aware type systems, resource lifetimes in Rust). Review agents can focus on semantic correctness, architectural compliance, and security implications rather than hunting for type errors.

**Security** — typed languages prevent certain vulnerability classes (type confusion, buffer overflows in memory-safe languages). Boundary typing for consumed dependencies reduces supply chain risk by making the contract explicit and testable.

**Repo readiness** — type safety could be a readiness criterion alongside test coverage and CI maturity. A repo with strong static typing and comprehensive integration tests is safer for agent autonomy than one relying entirely on runtime validation.

## Open questions

- Should type safety be a hard requirement for new services, or a strong preference with room for exceptions?
- How do we handle repos that are polyglot (e.g., Go controllers with shell scripts, Python utilities alongside React frontends)? Is type safety repo-level or component-level?
- Can gradual typing systems (TypeScript, Python with mypy) provide sufficient safety for agentic development, or do they retain too much of the dynamic language risk?
- What's the migration path for existing services written in dynamically typed languages? Is there a threshold where rewriting becomes worthwhile?
- How do we measure "agent compatibility" empirically? Can we compare agent error rates or refactoring success rates across languages?
- Should consumed dependencies have a "compatibility classification" based on how well-defined their boundaries are, affecting the autonomy level for changes that touch them?
