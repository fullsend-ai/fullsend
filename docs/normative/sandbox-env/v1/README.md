# Sandbox environment variable contract v1

Which environment variables the fullsend runner injects into the agent
sandbox, under what conditions, per which runtime, which arrive only
through stock harness configuration, and which the runner reserves from
harness override. The contract is published as generated JSON Schemas
(Draft 2020-12), so a consumer — the agents repo, a custom-agent repo, any
CI job — validates with stock tooling against a released runner instead of
a hand-maintained mirror
([ADR 0146](../../../ADRs/0146-sandbox-environment-contract-manifest.md)).

**Scope: the sandbox only.** Host-side pre-scripts, post-scripts, and
validation scripts run on the runner, not in the sandbox; their
environment is the runner's own plus `env.runner`, and this contract does
not describe it.

## Schemas

| File | Validates | Use it to |
|------|-----------|-----------|
| [`harness-env.schema.json`](harness-env.schema.json) | A harness YAML (converted to JSON) | Reject `env.sandbox` keys the runner reserves, in any letter case — at the top level, under `forge.<name>.env`, and under `overlays[].env` |
| [`env-references.schema.json`](env-references.schema.json) | `{"runtime": ..., "variables": [...]}` | Check that code running under a runtime reads only variables reachable there; its `$defs` are the full inventory |

Each schema is self-contained: every definition it uses is in its own
`$defs`, so validating needs one fetched file and no cross-file `$ref`
resolution.

### Validating a harness

```sh
check-jsonschema --schemafile "$BASE/harness-env.schema.json" harness/*.yaml
```

A reserved key fails with the offending path:

```text
$.env.sandbox: 'TRACEPARENT' should not be valid under {'$ref': '#/$defs/reserved_any_case'}
```

The runner upper-cases a key before checking it, so `traceparent` and
`Https_Proxy` are refused too; `#/$defs/reserved_any_case` matches the
reserved names in any letter case, while `#/$defs/reserved` keeps the
canonical uppercase inventory. The schema constrains only the env blocks;
other harness fields pass through. `fullsend lint` performs the same reserved-key check in-process
for repos that run the fullsend binary.

### Validating env references

List the variables a piece of sandbox code reads — a skill script, a
sandbox hook — minus the keys the harness's own `env.sandbox` declares
(those are reachable by construction), and name the runtime it runs
under:

```json
{"runtime": "pi", "variables": ["FULLSEND_OUTPUT_DIR", "FULLSEND_TARGET_REPO_DIR"]}
```

`runtime` is one of the production runtimes, or `portable` for code that
must work under every runtime — a skill shared across agents should use
`portable`. A skill that reads `CLAUDE_CONFIG_DIR` validates under
`claude` and fails under `pi` or `portable`, which is the
[fullsend-ai/agents#1672](https://github.com/fullsend-ai/agents/issues/1672)
failure caught at PR time.

## Inventory (`env-references.schema.json` `$defs`)

| Definition | Contents |
|------------|----------|
| `always` | Exported by the runner into every sandbox it builds. |
| `conditional` | Exported only when a gate holds. Each entry is `{"const": NAME, "description": GATE}`, the gate transcribed from the emitter's guard. |
| `runtime_<name>` | Exported only under that runtime: its `EnvExports()` plus the environment its launch command sets, including names a launch sets only on some paths (an inference gateway, a provider). |
| `harness_delivered` | Delivered by stock harness configuration, not the runner. Each entry's description names the configuration. |
| `reserved` | Refused in harness `env.sandbox`; the runner drops them instead of exporting them. |
| `deprecated` | Scheduled for removal; still reachable through their export category until then. |
| `runtime` | The accepted `runtime` values: `portable` plus each production runtime. |

The categories form a guarantee ladder; read them as answers to "what may
I rely on?", not as a flat list.

**`always` and `conditional` — runner-guaranteed, modulo gates.** Within
their gates these are reachable under any runtime, which is why
`portable` admits them. The security-hook variables are the exception:
only the claude, codex, and pi bootstraps append hook env, so
`TIRITH_FAIL_ON`, `TIRITH_REQUIRED`, and `FULLSEND_EGRESS_ALLOWLIST` never
reach an opencode sandbox. Their gate descriptions say so, and the
per-runtime rules enforce it: those names validate under claude, codex,
and pi, but not under opencode or `portable`.

**`runtime_<name>` — guaranteed only when that runtime runs.** Across
runtimes they are a portability gap: nothing promises `CLAUDE_CONFIG_DIR`
under codex or pi. A name a shared category already covers (`PATH`, or
hook env a launch command re-exports) stays in the shared category.

**`harness_delivered` — configuration, not guarantee.**
`GOOGLE_APPLICATION_CREDENTIALS`, `CLAUDE_CODE_USE_VERTEX`,
`ANTHROPIC_VERTEX_PROJECT_ID`, and `CLOUD_ML_REGION` reach the sandbox
because the `fullsend agent new` Vertex scaffold delivers them through
`env.sandbox` plus a host file; fleet harnesses deliver these plus
`GOOGLE_CLOUD_PROJECT` and `ANTHROPIC_DEFAULT_SONNET_MODEL` through
`env/gcp-vertex.env` in fullsend-ai/agents. A hand-authored harness that
omits that configuration will not see them.

**`reserved` — refused from harness override.** The runner keeps harness
configuration from shadowing runner infrastructure, injecting OIDC and
provider credentials, or overriding security-critical process variables
(`HTTPS_PROXY`, `LD_PRELOAD`, `NODE_OPTIONS`, …). A name may be both
exported and reserved — `PATH` is set by the runner *and* refused from
the harness. Exports answer "does the runner set it?"; `reserved`
answers "may the harness set it?".

**`deprecated` — scheduled for removal.** A deprecated name stays in its
export category for at least one minor release after it is first listed
here, then moves out of both. Consumers should warn on a match against
`#/$defs/deprecated` and migrate. The definition matches nothing while no
deprecations are active.

## Fetching

The schemas are committed alongside this README, so every tag serves them
at the sibling path over HTTPS — no authentication, no repository code
execution:

```text
BASE=https://raw.githubusercontent.com/fullsend-ai/fullsend/refs/tags/<version>/docs/normative/sandbox-env/v1
```

**The tag is the version.** The schemas carry no fullsend version: a
committed file cannot know its future release, and stamping one at release
time would break the golden invariant below. The `$id`
(`https://fullsend.ai/normative/sandbox-env/v1/…`) names the schema, as
for the other normative schemas; it is not a fetch location.

Validation cadence:

- **At PR time, validate against the latest released tag** — what
  production runs, so the contract a change must satisfy.
- **Nightly, validate against `main`** (`refs/heads/main` in the URL).
  Released tags lag `main`; the nightly check catches drift within a day
  ([ADR 0139](../../../ADRs/0139-agent-functional-test-suite-lives-in-agents-repo.md)).

Tags released before the contract landed return `404` for these paths.

## Treating the schemas as data

The schemas arrive over the network. Treat them as untrusted input:

- **Validate with a JSON Schema validator; never evaluate.** Do not `eval`
  fetched content or interpolate it into a shell command or template.
- **Fail closed.** A failed fetch, a non-2xx response, or a body that does
  not parse or is not a valid Draft 2020-12 schema MUST fail the consumer
  gate. An unverifiable contract is a failed check, not a skipped one.

The schemas carry names and gate descriptions only — never values — so
they are safe to fetch and inspect.

## Regeneration

The schemas are generated, never hand-authored. The collector in
`internal/cli/env_manifest_collector_test.go` runs every code path that
injects environment variables into the sandbox — the bootstrap `.env`
emission, the per-iteration env, trace-ID injection, the security-hook
env, every production runtime's `EnvExports()` and launch-command
exports, the `fullsend agent new` Vertex scaffold, and the effective
reserved-key set after the init-time merges. It replays the commands that
write sandbox env files in a scratch workspace and reads the resulting
files with an export-aware parser (`envmanifest.ExportedNames`), so a
quoted value or a dropped `export` keyword changes the result.
`TestLaunchEnvExportsMatchLaunchCommands` in `internal/runtime` pins each
runtime's declared launch exports to its built launch commands across
hooks, gateway, and provider variants.

Any change to those emitters changes the schemas. Regenerate:

```sh
go test ./internal/cli -run TestEnvManifestGolden -update
```

The always-on `TestEnvManifestGolden` byte-compares each committed schema
with collector output, and `TestEnvManifestRuntimeEnumeration` fails when
a runtime is registered without a contract decision. Output is
deterministic — fixed key order, sorted names, trailing newline.

## Versioning

Breaking changes to the schemas' shape require
`docs/normative/sandbox-env/v2/`.

| Change | Version | Consumer impact |
|--------|---------|-----------------|
| Rename or remove a `$defs` entry, change the `env-references` instance shape, redefine a category | v2 | Consumers must migrate |
| Add a variable or runtime to a category, refine a gate or provenance description, add a `deprecated` entry | v1 | None |
| Add a name to `reserved` | v1 | A harness setting it starts failing — the runner already drops it, so the schema reports an existing no-op |
| Remove a name from an export category | v1, after a `deprecated` window | Code reading it starts failing `env-references` validation |

Variable sets change with the code — that is the contract doing its job.
Consumers pin a tag and validate; a new failure is the drift signal.
