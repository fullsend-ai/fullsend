# Configuration source inventory

Working inventory for aligning layered configuration with
[ADR 0069](../ADRs/0069-ready-made-configuration-presets.md). A central
baseline belongs in `.fullsend/config.base.yaml`. A repository override
belongs in `.fullsend/config.yaml`. This list records, for each value a
person can set, where it can be set today, which source wins, and whether
the base file should be allowed to carry it.

The target order, from the configuration alignment work, is:

1. CLI flag for one run
2. `.fullsend/config.yaml`
3. `.fullsend/config.base.yaml`
4. Environment or repository variable, kept so older workflows and binaries still run
5. Compiled default

This order does not apply to `runtime`, `agents[].model`, and
`agents[].effort`: Accepted [ADR 0091](../ADRs/0091-per-agent-runtime-model-effort.md)
already decided flag > `FULLSEND_*` env > the agent's `agents:` entry >
repo-wide `runtime:`/harness > default for exactly those three fields —
environment stays above the file. The "Wins today" cells for those rows
describe that accepted order, not a bug. Moving them under the order
above would need an ADR amendment, not a code change.

File merge rules for values that already have a YAML field are in the
[layered config reference](../guides/infrastructure/layered-config-reference.md).
This page does not repeat those rules. It records which source wins before
the accessor runs.

## How a workflow run gets a value

Two paths run side by side.

**The binary reads the layered files.** `fullsend run` loads
`config.yaml` over `config.base.yaml` through accessors. For runtime,
model, and effort it consults that result only after the CLI flag and the
`FULLSEND_*` environment variables are empty
(`resolveRunOverrides` in `internal/cli/run_overrides.go`).

**The workflow injects a copy.** `github setup` writes mint URL, inference
region, project id, WIF provider, and the review client id into the
layered files and also into repository variables and secrets, because
current workflow templates still read the variables
(`internal/cli/github.go`). Caller workflows pass `vars.FULLSEND_MINT_URL`
and `vars.FULLSEND_GCP_REGION` into the reusable workflow. Runtime, model,
and effort do not come from `github setup`: `setup-agent-env.sh` copies
its own separate allowlist of `FULLSEND_*` repository variables into the
job environment. The OpenAI Workload Identity variables come from a
separate opt-in step, `fullsend inference openai import --variables`.
Dispatch steps in `reusable-dispatch.yml` also `yq` `.fullsend/config.yaml`
directly and do not read `config.base.yaml`.

A value set only in the base file is invisible to a reader that looks at
the repository variable or at `config.yaml` alone.

## Decision words

| Decision | Meaning |
|---|---|
| Already layered | The Go accessor uses overlay, then base, then the compiled default. No workflow variable overrides it. |
| File should win | A file field exists. Change the binary so the file beats the variable. Keep the variable as a fallback when the file leaves the field unset. Stop passing the workflow flag only after released binaries read the file. |
| Layer the direct read | The Go accessor is layered, and a workflow script still reads `config.yaml` alone. Teach that script the merged value before a base-file setting can take effect. |
| Add a file field | The value is configuration and has no YAML field. Add one, then apply "file should win". |
| Leave as a variable or secret | A file must not carry it. The reason is on the row. |

"Base file?" is yes when a central admin should be able to push the same value to every repository through `config.base.yaml`.

## File fields the Go accessor already layers

These fields live on `perRepoConfig` in `internal/config/config.go`. Unset means fall through to the base file, then to the compiled default. No `setup-agent-env.sh` allowlist entry and no reusable-workflow input overrides them.

| Setting | Controls | Set from | Wins today | Read by | Decision | Base file? |
|---|---|---|---|---|---|---|
| `version` | Config schema version | Overlay, base, compiled `"1"` | Overlay, then base, then `"1"` | Config loader | Already layered | No. It is the schema version, not an admin policy. |
| `forge` | Hosting platform (`github` or `gitlab`) | `--forge`, overlay, base, compiled empty (GitHub) | `--forge`, then overlay, then base. If still empty, CI detection (`GITHUB_ACTIONS`/`GITLAB_CI`) fills it; outside CI it can stay empty | CLI | Already layered. The flag is a one-shot override and stays first. | Yes, when a fleet is single-forge. |
| `tracker` | Default for `fullsend issues --tracker` | Overlay, base, compiled empty (flag required) | `--tracker`, then the layered file | `fullsend issues` | Already layered. The flag is a one-shot override and stays first. | No. Trackers differ per repository. |
| `keep_history` | Whether sticky comments keep a collapsed previous body | `--keep-history`, overlay, base, compiled `true` | `--keep-history` (when the flag was explicitly passed), then overlay, then base, then `true` | Comment updaters | Already layered. The flag is a one-shot override and stays first. | Yes, when a fleet wants one comment style. |
| `allowed_remote_resources` | URL prefixes agents may fetch | Overlay, base, compiled fullsend and agents prefixes | Overlay union or deny-all, then base, then the compiled prefixes | Resource fetch | Already layered | Yes. This is the shared fetch allowlist. |
| `create_issues` | Repositories agents may open issues in | Overlay, base, compiled unset | Whole object replaces the parent when set | Issue creation | Already layered | Yes for a shared allowlist. A repository overlay replaces the whole object. |
| `status_notifications` | Start and completion comments and reactions | Overlay, base, compiled unset | Whole object replaces the parent when set | Status posting | Already layered | Yes, when a fleet wants one notification policy. |
| `models.aliases` | Remap of `opus`, `sonnet`, `haiku`, `fable` | Overlay, base, compiled alias table | Per-key merge, then the compiled table | Model resolution | Already layered | Yes. Fleet model pins belong here. |
| `inference.provider` | Inference backend id | Overlay, base, compiled `vertex` | Overlay, then base, then `vertex`. No workflow variable found | Accessor at install and run setup | Already layered | Yes. |

`authorization` (extra permission sources, today `owners_file`) is
overlay-only by design, not an accessor gap: a base-file value under this
key is ignored, and the bash check in `reusable-dispatch.yml` also reads
only `.fullsend/config.yaml`, so the two reads agree. It does not fit the
"already layered" table above, because that table's unset-falls-through
rule does not hold for this key. Decision: leave overlay-only. Each
repository opts in explicitly, and a base-file default would silently
enroll every repository. Base file: no.

## File fields a workflow reads too narrowly

The Go accessor layers these. The bash steps in `.github/workflows/reusable-dispatch.yml` read `.fullsend/config.yaml` and skip `config.base.yaml`. A baseline that exists only in the base file does not affect those steps. If `config.yaml` is missing, the role check exits without applying a base-file role list.

| Setting | Controls | Set from | Wins today | Read by | Decision | Base file? |
|---|---|---|---|---|---|---|
| `kill_switch` | Stop all agent dispatch | Overlay, base, compiled `false` | Go accessor: overlay, then base, then `false`. Bash "Check kill switch": `yq '.kill_switch'` on `config.yaml` only. A missing file does not halt dispatch | `internal/harnessdispatch` and `reusable-dispatch.yml` | Layer the direct read so a base-file `true` halts dispatch | Yes. A central stop belongs in the base file. |
| `roles` | Which agent roles are enabled | Overlay, base, compiled default role list | Go accessor: replace-if-set, so an explicit empty `roles: []` denies every role. Bash "Check role is enabled": `yq '.roles[]'` on `config.yaml` only, piped through `\|\| echo ""`. An omitted key, a missing file, an explicit `roles: []`, or any `yq` error all leave `$ROLES` empty, and the script only skips the stage when `$ROLES` is non-empty — so all four cases fail open and let the stage run, the opposite of the Go accessor's deny-all | Go accessor and `reusable-dispatch.yml` | Layer the direct read, and make the bash script fail closed on an explicit empty list, not just add the base file | Yes, for the shared role set. An overlay list replaces it. |
| `agents` | Registered agents and per-agent `runtime`, `model`, `effort`, `subagents` | Overlay, base, compiled none | Go accessor: keyed merge by agent name. Bash validates the shape of `config.yaml` only | Go run path and `reusable-dispatch.yml` | Layer the direct read for enablement and role checks. Per-agent runtime, model, and effort still lose to flags and environment variables; those rows are below | Yes, for the shared agent baseline. |

## File fields that lose to a variable or a flag

`github setup` stores these in the layered files and copies them to repository variables or secrets so existing workflows keep working (`internal/cli/github.go`). On a live run the workflow reads the copy.

| Setting | Controls | Set from | Wins today | Read by | Decision | Base file? |
|---|---|---|---|---|---|---|
| `runtime`, and per-agent `agents[].runtime` | Which runtime runs (`claude`, `pi`, `codex`, `dummy`) | `--runtime`, `FULLSEND_RUNTIME` (repository variable copied by `setup-agent-env.sh`), agent entry, repo `runtime:`, compiled `claude` | Flag, then `FULLSEND_RUNTIME`, then the agent entry, then repo `runtime:`, then `claude` (`docs/cli/run.md`) | `fullsend run` | File should win. Flag stays first. `FULLSEND_RUNTIME` stays as the fallback | Yes, for the fleet default. |
| `agents[].model` | Model for one agent | `--model`, `FULLSEND_MODEL`, then `FULLSEND_PI_MODEL` or `FULLSEND_CODEX_MODEL` when that runtime is selected, agent entry, harness, frontmatter | Flag, then `FULLSEND_MODEL`, then the runtime-scoped alias, then the agent entry | `fullsend run` | File should win. Same fallback rule | The shared default can live on the base-file agent entry. |
| `agents[].effort` | Effort for one agent | `--effort`, `FULLSEND_EFFORT`, agent entry, harness | Flag, then `FULLSEND_EFFORT`, then the agent entry | `fullsend run` | File should win. Same fallback rule | Same as model. |
| `mint_url` | Token mint address | `--mint-url`, `FULLSEND_MINT_URL`, layered `mint_url`, compiled `https://mint.fullsend.sh` | On `fullsend run`: flag, then `FULLSEND_MINT_URL`. The accessor is not consulted on that path. Workflows pass `vars.FULLSEND_MINT_URL` in as the `mint-url` input, so the flag is set | `fullsend run`, mint-token action. GitLab jobs never mint; `forgePlatform == "gitlab"` skips that path throughout `run.go`, and the GitLab scaffold has no mint reference | File should win once the binary reads `mint_url` before the variable. The workflow must keep passing `mint-url` until old binaries read the file. Setup already copies the effective file value into `FULLSEND_MINT_URL` | Yes. |
| `inference.region` | GCP region for inference | Layered `inference.region`, compiled `global`. Workflows pass `vars.FULLSEND_GCP_REGION` as `gcp_region`, which becomes `CLOUD_ML_REGION`. GitLab `run-agent-job.sh` exports `CLOUD_ML_REGION` from `FULLSEND_GCP_REGION` | The job environment, not the file. Setup writes the file's effective region into `FULLSEND_GCP_REGION` at install time. A later edit to the file does not change the variable | Vertex client via `CLOUD_ML_REGION`. The accessor is used at install and converge | File should win for the binary. Keep `FULLSEND_GCP_REGION` as the fallback and as the value old workflows still pass | Yes. |
| `inference.project` | GCP project for inference | Layered `inference.project` (no compiled default), secret `FULLSEND_GCP_PROJECT_ID` | The secret. Workflows export it as `ANTHROPIC_VERTEX_PROJECT_ID`. `setup-gcp` uses the same secret to authenticate before the binary runs | Google auth action and the Vertex client | Leave the secret for the auth action. The file should be what the binary reads, with the secret as fallback. Do not put the secret value only in git if the auth action still needs it | Yes for the project id the binary uses. The secret remains for the auth action. |
| `inference.wif_provider` | Workload Identity provider resource name | Layered `inference.wif_provider` (no compiled default), secret `FULLSEND_GCP_WIF_PROVIDER` | The secret. `setup-gcp` reads it | Google auth action | Leave the secret for the auth action. The file should describe the provider the binary needs. Same split as the project id | Yes for the non-secret resource name. The credential material stays a secret. |
| `inference.openai.audience`, `identity_provider_id`, `service_account_id` | OpenAI Workload Identity identifiers. Not secrets | Layered `inference.openai`, or the three `FULLSEND_OPENAI_*` repository variables | If any `FULLSEND_OPENAI_*` variable is set, all three come from variables and the file is ignored (`resolveOpenAICredential` in `internal/cli/run_openai.go`, on the `fullsend run` path). Workflows inject `vars.FULLSEND_OPENAI_*` on every run. Static `OPENAI_API_KEY` / `FULLSEND_OPENAI_API_KEY` applies only when the trio is unset | `fullsend run` | File should win as a whole trio. A partial variable set must not hide a complete file block. The API key stays a secret | Yes for the three identifiers. |

## Values with no file field

| Setting | Controls | Set from | Wins today | Read by | Decision | Base file? |
|---|---|---|---|---|---|---|
| `FULLSEND_FALLBACK_MODELS` | Extra models to try when the first alias is unavailable | Repository variable, copied by `setup-agent-env.sh` | The environment variable. No YAML field | `fullsend run` for Claude and pi | Add a file field if a fleet should share the chain. Until then the variable is the only source | Yes, once a field exists. |
| `FULLSEND_PI_PROVIDER` | pi backend (`anthropic-vertex`, `openai`, `xai-vertex`, …) | Repository variable, copied by `setup-agent-env.sh` | The environment variable. No YAML field | pi runtime | Add a file field. The variable stays as the fallback | Yes, once a field exists. |
| `FULLSEND_PI_MODEL`, `FULLSEND_CODEX_MODEL` | Runtime-scoped aliases of the model override | Repository variables, copied by `setup-agent-env.sh` | After `FULLSEND_MODEL`, before the agent entry, and only when that runtime is selected | `fullsend run` | No separate file field. They are compatibility names for `agents[].model` | No. |
| `REVIEW_FINDING_SEVERITY_THRESHOLD` | Lowest review finding severity to post | Agent environment variable (`docs/agents/review.md`) | The variable. No YAML field in this repo | Review agent | Leave as a variable until an agent-settings field exists. It tunes one agent, not install | Only if agent settings move into the base file later. |
| `CODE_AUTO_MERGE`, `CODE_AUTO_MERGE_METHOD` | Whether the code agent enables GitHub auto-merge, and the method | Agent environment variables (`docs/guides/user/adoption.md`) | The variables. No YAML field | Code agent | Leave as variables, same reason as the review threshold | Same. |
| `OTEL_EXPORTER_OTLP_*`, `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_SDK_DISABLED`, `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` | Trace export | Repository variables. Header values are secrets | The variables. Unset means export stays off | CLI tracer, from the workflow environment | Leave as variables. Endpoints differ per environment, and headers are secrets | No. |
| `FULLSEND_SANDBOX_IMAGE`, `FULLSEND_SANDBOX_ARCH` | Sandbox image and CPU architecture for a run | Process environment | The variables, when set | `fullsend run` | Leave as variables. They are per-machine or per-job overrides | No. |
| `FULLSEND_REVIEW_CLIENT_ID` | Review app client id, used to check prior review comments | Repository variable written by `github setup` | The variable | Review pre-fetch script | Leave as a variable. Setup derives it from the installed app. It is not a preset input | No. |

## Left out on purpose

These are credentials or CI plumbing. They do not belong in `config.base.yaml`.

- Tokens and API keys: `GH_TOKEN`, `GITHUB_TOKEN`, `GITLAB_TOKEN`, role PATs (`FULLSEND_GITLAB_POLLER_TOKEN` and the analyst and coder equivalents), `FULLSEND_FORGE_TOKEN`, `FULLSEND_OPENAI_API_KEY`, `OPENAI_API_KEY`, `JIRA_TOKEN`.
- GitLab install machinery: `FULLSEND_DISPATCH_SECRET`, `FULLSEND_TRIGGER_TOKEN`, `FULLSEND_WEBHOOK_SECRET`, `FULLSEND_GITLAB_ROLE_MIGRATION`, `FULLSEND_GITLAB_ROLE_REGISTRY`, `FULLSEND_GITLAB_ROLE_ROTATION`.
- One-run context the workflow already knows: issue and PR numbers, `GITHUB_SHA`, `TRACEPARENT`, `FULLSEND_DIR`.
