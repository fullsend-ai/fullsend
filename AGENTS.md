# AGENTS.md

Fullsend is a platform for fully autonomous agentic development for Git-hosted
organizations (GitHub, GitLab, Forgejo). It contains design documents organized
by problem domain (`docs/`) and a Go CLI (`cmd/fullsend/`) that manages forge
setup and org configuration. See [fullsend.sh](https://fullsend.sh) for the
full documentation site.

## How to work in this repo

- Problem documents (`docs/problems/`) should present multiple options with
  trade-offs, not prescribe single solutions.
- Each problem document has an "Open questions" section — this is where
  unresolved issues live.
- When adding new problem areas, create a new file in `docs/problems/`. The
  documentation site auto-discovers files in this directory.
- For each existing problem doc that a new problem doc links to, add a
  reciprocal contextual backlink at the passage that motivated the outbound link
  in the same PR. Then run `grep -rn '<concept>' docs/problems/`, searching
  adjacent terms and synonyms as well as the new doc's title words, to find
  related passages the new doc does not already link to; wherever an existing
  doc *substantively discusses* the concept (not merely mentions it in passing),
  add a single contextual backlink at that passage, not a link at every match.
  This keeps cross-referencing bidirectional so readers can discover the new doc
  from its related contexts. When the new doc is organization-specific
  (`docs/problems/applied/<org-name>/`), frame any backlink you add to a core
  problem doc as an explicit org-specific pointer, so core docs stay
  organization-agnostic (see the organization-agnostic rule below).
- The security threat model (threat priority: external injection > insider >
  drift > supply chain) should inform all other documents.
- Keep core problem documents organization-agnostic. Organization-specific
  details belong in `docs/problems/applied/<org-name>/`.
- The target audience for problem documents is any contributor community
  considering autonomous agents — keep language accessible and avoid presuming
  solutions.
- Always stage changes before running `make lint`. Pre-commit only checks
  staged files; without staging first, it stashes your work and finds nothing
  to lint.
- When invoking the fullsend CLI from this checkout, always prefer
  `go run ./cmd/fullsend …` from the repo root over an external or pre-built
  binary to avoid stale state. See
  [Go Code § Running the fullsend CLI](docs/contributing/go-code.md#running-the-fullsend-cli).
- Follow [COMMITS.md](COMMITS.md) when writing or reviewing commit messages
  and PR titles. Commit prefixes feed release notes; breaking changes require
  the `!` marker in both commit subjects and PR titles; a missing `!` is an
  important-severity review finding; forbidden combinations (e.g.
  `fix(ci)`/`fix(e2e)`) are enforced as errors.
- Human and human-driven agent commits require DCO sign-off (`git commit -s`);
  a local `commit-msg` hook enforces this (`SKIP_DCO=1` or
  `SKIP_COMMIT_MSG_HOOK=1` to bypass locally). Autonomous bot commits are
  exempt and must never supply the DCO with `-s` or `Signed-off-by`. See
  [CONTRIBUTING.md § DCO](CONTRIBUTING.md#dco-developer-certificate-of-origin).
- **Go coverage gate:** Go production changes (`cmd/`, `internal/`) must meet
  the 80% patch coverage target (75% floor). Verify coverage locally before
  committing; see
  [Go Code § Verifying patch coverage locally](docs/contributing/go-code.md#verifying-patch-coverage-locally).
- **Forge abstraction:** All forge operations must go through `forge.Client`;
  never shell out to `gh` or make direct API calls outside
  `internal/forge/github/`. See
  [Forge Abstraction](docs/contributing/forge-abstraction.md).
- Never commit secrets (tokens, API keys, PEM keys, gcloud credentials) or
  sensitive data (GCP project names, service account identifiers, Model Armor
  template names, internal hostnames). Use environment variables with no
  defaults for sensitive values.
- **Experiments issues:** File experiment source changes in
  [`fullsend-ai/experiments`](https://github.com/fullsend-ai/experiments);
  submodule integration and fullsend product work stay in this tracker. See
  [CONTRIBUTING.md § Where to file experiments-related issues](CONTRIBUTING.md#where-to-file-experiments-related-issues).
- When adding a new doc under `docs/`, update `docs/.vitepress/config.ts`
  sidebar config and search scopes if not in an auto-discovered section. See
  [Documentation § Adding new documentation pages](docs/contributing/documentation.md#adding-new-documentation-pages).
- When modifying CLI commands, flags, public APIs, or configuration/environment
  variables, update all documentation references across `docs/cli/` and
  `docs/guides/`. Note that `docs/cli/` reference pages are manually maintained
  and will not auto-update. See
  [Documentation](docs/contributing/documentation.md).
- When extending a Go interface with new methods, see
  [Go Code § Interface documentation](docs/contributing/go-code.md) for sync
  requirements.
- When adding a new skill under `skills/`, check user-facing guides
  (`docs/guides/user/customizing-with-skills.md`,
  `docs/guides/user/bring-your-own-agent.md`, `docs/guides/README.md`) for
  cross-reference opportunities.
- When modifying fields in `event_payload` (`buildEventPayload` in
  `internal/harnessdispatch/project.go`) or normalized-event structures
  (`internal/normevent/`), update the projection table in
  `docs/normative/normalized-event/v1/README.md` and resolution details in
  [Harness Field Reference](docs/contributing/harness-fields.md).
- Document every new `config.yaml` field in
  [Config Reference](docs/reference/config-reference.md).
- **Per-org installation mode is deprecated**
  ([ADR 0044](docs/ADRs/0044-deprecate-per-org-installation-mode.md)) in favor
  of per-repo mode; do not add or extend org-mode content, and flag org-mode
  references in PR reviews.
- Discover agent/skill/sub-agent inventory dynamically from
  `fullsend-ai/agents` rather than hardcoding static lists. See the
  `author-fullsend-augmentations` skill for discovery patterns.

## Topic-specific guidance

Detailed guidance lives in `docs/contributing/` and topic-specific guides under `docs/guides/`. Read only the file relevant to your current task — do not read all of them.

| File | When to read |
|------|-------------|
| [Runtime Implementation](docs/contributing/runtime-implementation.md) | Adding or changing a `runtime.Runtime` backend — covers the security feature matrix every runtime must fill in, the runtime interfaces, the sandbox hook contract and wire protocol, and the sandbox workspace layout |
| [Go Code](docs/contributing/go-code.md) | Changing Go code under `cmd/`, `internal/`, or `pkg/` — covers mint sync, the behaviourtest/mintcore module-boundary rule, interface documentation sync, coverage, vet, e2e tests, concurrency testing, concurrent error handling (`errors.Join` fan-out), context-aware blocking, suite-timeout policy, WASM binary size constraints, credential redaction for external content, secure HTTP client construction, preferring `go run` for the CLI, and the per-repo config dual-validation pattern |
| [Mintcore Architecture](docs/contributing/mintcore.md) | Changing `internal/mintcore/`, `cmd/mint-wasm/`, `cmd/mint/`, `internal/mint/`, or mint deploy provisioners (`internal/dispatch/gcf/`, `internal/dispatch/cf/`) — covers platform accessors, load-site construction, WASM-safe wiring, and deploy-time build-metadata stamping |
| [Behaviour Testing](docs/guides/dev/behaviour-testing.md) | Modifying behaviour test repo provisioning, fork handling, or workflow dispatch — covers forge API constraints (`auto_init`, fork name derivation, fork name uniqueness lag, Actions readiness, CI timeout budgeting) |
| [Workflow Contracts](docs/contributing/workflow-contracts.md) | Changing GHA reusable workflows — covers dispatch routing, secret/input threading across caller chains, and review rules |
| [Shell Scripting](docs/contributing/shell-scripting.md) | Writing or reviewing shell scripts — covers `gh api --paginate` pitfalls, jq patterns, stdout contamination in command substitution, bot permissions, and concurrent label deletion |
| [Forge Abstraction](docs/contributing/forge-abstraction.md) | Adding forge operations — covers `forge.Client` interface rules |
| [Harness Composition](docs/contributing/harness-composition.md) | Changing merge functions in `internal/harness/` — covers the invariant between compose and forge merge functions |
| [Harness Field Reference](docs/contributing/harness-fields.md) | Adding or modifying fields in `Harness` or `ForgeConfig` — covers field classifications, merge rules, and the `ForgeConfig` struct |
| [Config Reference](docs/reference/config-reference.md) | Adding or modifying fields in `config.yaml` (`internal/config/`) — covers all per-repo config fields, valid values, defaults, and links to detailed guides |
| [Per-repo AgentEntry fields](docs/contributing/harness-composition.md) | Adding or modifying fields in `AgentEntry` — covers layered merge handling, tests, documentation, and override-only semantics |
| [Normative Event Specification](docs/normative/normalized-event/v1/README.md) | Adding or modifying fields in `event_payload` (`buildEventPayload`), normalized-event structures under `internal/normevent/`, or adapters — covers the projection table, transition vocabulary, adapter contracts, and CEL trigger examples |
| [CEL Triggers](docs/contributing/cel-triggers.md) | Writing or reviewing harness `trigger` CEL expressions or `.feature` CEL filters — covers normalized transition kinds |
| [ADRs](docs/contributing/adrs.md) | Touching `docs/ADRs/` or reviewing ADR changes — covers immutability and status rules, required forge scope, and code-cited descriptions of current behavior |
| [Sandbox Topology](docs/contributing/sandbox-topology.md) | Modifying sandbox images, CI image pulling, or agent harness configs |
| [Bot Identities](docs/contributing/bot-identities.md) | Referencing bot identities in code — covers GitHub App logins and shared identities |
| [GitLab Role Credentials](docs/contributing/gitlab-role-credentials.md) | GitLab registered-role credential contract (built-in Poller/Analyst/Coder plus administrator custom roles), trusted registry, role-identity state, selection rules, and credential-routing security checklist |
| [Design Decisions](docs/contributing/design-decisions.md) | Understanding architectural principles and key decisions |
| [Vouch System](docs/contributing/vouch-system.md) | Working with the contributor vouch gate or PR workflows |
| [Tier Conventions](docs/contributing/tier-conventions.md) | Using the term "tier" in code or docs — covers the three distinct tier contexts |
| [CI Workflows](docs/contributing/ci-workflows.md) | Adding or modifying GitHub Actions workflows under `.github/workflows/`, adding secrets to `pull_request_target` jobs, or reviewing refactors across inline-step, reusable-workflow, and composite-action boundaries for context-variable scoping |
| [Documentation](docs/contributing/documentation.md) | Writing or editing Markdown prose (line wrapping rule), or adding, removing, renaming, or changing CLI commands, flags, configuration variables, or environment variables — covers cross-reference of CLI command groups to all documentation touchpoints |
