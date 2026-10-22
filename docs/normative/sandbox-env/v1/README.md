# Sandbox environment contract v1

Which environment variables the fullsend runner injects into the agent
sandbox, under what conditions, per which runtime, which arrive only through
stock harness configuration, and which the runner reserves from harness
override. The contract is published as a generated, versioned,
machine-readable manifest so consumers can verify env-var reachability
against a released runner instead of hand-maintained mirrors — the
transitional lockstep between fullsend and the fleet harnesses it loads
([ADR 0058](../../../ADRs/0058-agent-registration.md)) becomes verifiable
rather than assumed
([ADR 0146](../../../ADRs/0146-sandbox-environment-contract-manifest.md)).

## Contract

- **Manifest:** [`sandbox-env.json`](sandbox-env.json) — the committed,
  generated contract document. It is authoritative; this README describes it.
- **Generated, never hand-authored:** the collector in
  `internal/cli/env_manifest_collector_test.go` derives the manifest by
  running every code path that injects environment variables into the
  sandbox — the bootstrap `.env` emission, the per-iteration env,
  trace-ID injection, the security-hook env, every production
  runtime's `EnvExports()`, the `fullsend agent new` Vertex scaffold,
  and the effective reserved-key set after the init-time merges.
- **Golden invariant:** `internal/cli/env_manifest_golden_test.go`
  byte-compares the committed file against collector output on every test
  run, so the published contract cannot drift from the code that builds the
  sandbox environment. A hand edit, or a code change shipped without
  regenerating, fails the test.
- **Names only, never values:** the manifest records variable names,
  category membership, gate descriptions, and provenance — no credentials or
  configuration values. Every name is already public in fullsend's source.

## Schema

| Field | Type | Semantics |
|-------|------|-----------|
| `schema_version` | integer | Shape version of this document; currently `1`. Check it before interpreting anything else — a version the consumer does not understand is a contract it cannot validate. |
| `exports.always` | array of strings | Variables the runner injects into every sandbox it builds (four in v1). |
| `exports.conditional` | object: variable name → gate description | Variables the runner injects only when the described gate holds (twelve in v1). Each description is the emitter's actual guard, transcribed from the production code. |
| `exports.runtime_specific` | object: runtime name → array of strings | Variables emitted only by the named runtime's `EnvExports()`: `claude`, `codex`, `opencode`, `pi`. |
| `harness_delivered` | object: variable name → `{ "provenance": string, "runner_guaranteed": boolean }` | Variables delivered by stock harness configuration, not by the runner (four in v1). `runner_guaranteed` is always `false` — a label, not a toggle: a variable the runner guarantees belongs in `exports` instead. `provenance` names the configuration that delivers it. |
| `reserved` | array of strings | Keys the runner reserves from harness override (62 in v1): `env.sandbox` cannot set them. The set merges the base runner-infrastructure keys, the OIDC-denied and provider-only credential keys, and the security-critical credential keys (`IsReservedCredentialKey`) at startup. |
| `deprecated` | array of strings | Variables that are deprecated and scheduled for removal in a future version. Consumers should migrate away from these variables. Empty when no deprecations are active. |

Abridged shape (see [`sandbox-env.json`](sandbox-env.json) for the full
document):

```json
{
  "schema_version": 1,
  "exports": {
    "always": ["FULLSEND_OUTPUT_DIR", "FULLSEND_ROLE", "PATH"],
    "conditional": {
      "FULLSEND_FETCH_URL": "the runtime fetch service is enabled (ADR 0038)"
    },
    "runtime_specific": {
      "claude": ["CLAUDE_CONFIG_DIR"],
      "opencode": ["OPENCODE_CONFIG_DIR", "..."]
    }
  },
  "harness_delivered": {
    "GOOGLE_APPLICATION_CREDENTIALS": {
      "provenance": "fullsend agent new Vertex scaffold (internal/agentnew); the fleet delivers the same variables through env/gcp-vertex.env in fullsend-ai/agents",
      "runner_guaranteed": false
    }
  },
  "reserved": ["BASH_ENV", "HOME", "LD_PRELOAD"],
  "deprecated": []
}
```

## Category semantics

The categories form a guarantee ladder. Consumers should read them as
answers to "what may I rely on?", not as a flat inventory.

**`always` and `conditional` — runner-guaranteed, modulo gates.** Every
fullsend runner injects the `always` variables into every sandbox it builds.
A `conditional` variable is guaranteed exactly when its gate holds — for
example, `FULLSEND_FETCH_URL` and `FULLSEND_FETCH_TOKEN` arrive when the
runtime fetch service is enabled
([ADR 0038](../../../ADRs/0038-universal-harness-access.md)), and
`FULLSEND_TRACE_ID` when trace-ID injection succeeds (injection is
best-effort; on failure the run warns and continues). Within their gates,
these two categories are universally reachable: a harness may rely on them
under any runtime.

**`runtime_specific` — guaranteed only when that runtime runs.** Both halves
matter:

- When dispatch executes under a runtime, that runtime's listed variables are
  guaranteed: a run under `claude` always has `CLAUDE_CONFIG_DIR`; a run
  under `pi` always has its six listed variables.
- Across runtimes, they are a portability gap, not a guarantee: nothing
  promises `CLAUDE_CONFIG_DIR` under `codex` or `pi`. A harness that reads a
  runtime-specific variable is coupled to that runtime and breaks when
  configuration changes the runtime.

The enumerated runtimes are the runner's resolution registry minus test
stubs. A runtime that exports nothing appears with an empty set — `opencode`
— rather than being omitted. Registering a new runtime without a manifest
decision fails the golden test.

**`harness_delivered` — configuration, not guarantee.** These variables reach
the sandbox only because stock harness configuration injects them:
`GOOGLE_APPLICATION_CREDENTIALS`, `CLAUDE_CODE_USE_VERTEX`,
`ANTHROPIC_VERTEX_PROJECT_ID`, and `CLOUD_ML_REGION` are delivered by the
`fullsend agent new` Vertex scaffold through `env.sandbox` plus a host file,
and fleet harnesses deliver the same variables through `env/gcp-vertex.env`
in fullsend-ai/agents (see each entry's `provenance`). The runner never
injects them — `runner_guaranteed: false` — so a hand-authored harness that
omits that configuration will not see them.

**`reserved` — stripped from harness override.** Harness `env.sandbox`
entries naming these keys are skipped with a warning: the runner removes
them so harness configuration cannot shadow runner infrastructure, inject
OIDC and provider credentials, or override security-critical process
variables (e.g. `HTTPS_PROXY`, `LD_PRELOAD`, `NODE_OPTIONS`). A key may appear both here and in an export
category — `PATH` is exported by the runner *and* reserved against harness
override. The categories answer different questions: `exports` asks "does
the runner set it?", `reserved` asks "may the harness set it?".

**`deprecated` — scheduled for removal.** Variables listed here still
function but are slated for removal in a future version. Consumers
should migrate to any documented replacement. The array is empty when no
deprecations are active.

## Fetching the manifest

The manifest is fetchable over HTTPS from raw.githubusercontent.com — no
authentication, no repository code execution. The manifest is committed
alongside this README, so every tag that serves this page serves the
manifest at the sibling path:

```text
https://raw.githubusercontent.com/fullsend-ai/fullsend/refs/tags/<version>/docs/normative/sandbox-env/v1/sandbox-env.json
```

**The tag is the version.** The manifest deliberately carries no
`fullsend_version` field: a committed file cannot know its future release
version, and stamping one at release time would break the golden invariant
(committed file must byte-match generator output). Consumers pin by URL.

Consumer validation cadence:

- **At PR time, validate against the latest released tag.** The released tag
  is what production actually runs, so it is the contract a proposed change
  must satisfy.
- **Nightly, validate against `main`**
  (`https://raw.githubusercontent.com/fullsend-ai/fullsend/refs/heads/main/docs/normative/sandbox-env/v1/sandbox-env.json`).
  Released tags lag `main`; the nightly `main` check is the bridge that
  catches contract drift within a day
  ([ADR 0139](../../../ADRs/0139-agent-functional-test-suite-lives-in-agents-repo.md)).

Tags released before the manifest landed (v0.48.1 and earlier) return `404`
for this path. A `404` means the pinned tag predates the contract; treat it
as a validation failure (below), not as an empty contract.

## Treating the manifest as data

The manifest arrives over the network from a remote host. Treat it as
untrusted input:

- **Parse, never evaluate.** Feed the bytes to a JSON parser and read names
  and gate descriptions as data. Never `eval`, never interpolate fetched
  content into a shell command or template.
- **Fail closed.** A fetch that fails, a non-2xx response, a body that does
  not parse, or a `schema_version` the consumer does not understand MUST
  fail the consumer gate — not pass it. A manifest that cannot be fetched or
  parsed is an unverifiable contract, and an unverifiable contract is a
  failed check, not a skipped one.

The manifest is itself a drift-control measure in fullsend's threat model;
publishing names and gate descriptions only — never values — keeps it safe
to fetch and inspect.

## Regeneration

Any change to the env-construction emitters changes the derived manifest.
Regenerate the committed file from collector output:

```sh
go test ./internal/cli -run TestEnvManifestGolden -update
```

The golden invariant: the committed manifest must byte-match collector
output. Serialization is deterministic — sorted names, stable field order,
trailing newline — so regenerating without a source change produces a
byte-identical file. The always-on check

```sh
go test ./internal/cli -run TestEnvManifestGolden
```

fails on any divergence between the committed file and the collector's
derivation of the production emitters.

## Versioning

Breaking changes require `docs/normative/sandbox-env/v2/`.

| Change | v1 impact |
|--------|-----------|
| **Breaking** (requires v2): remove or rename schema fields, change a field's type, remove or rename a category, redefine category semantics | Consumers must migrate |
| **Non-breaking** (allowed in v1): add variables or runtimes to any category, refine gate descriptions, add `harness_delivered` provenance entries, add or remove `deprecated` entries | Existing consumers keep working |

Variable sets change with the contract itself — that is the manifest doing
its job. Consumers pin a tag and compare; the comparison is the drift signal.
`schema_version` versions the document shape; a v2 directory bumps it.
