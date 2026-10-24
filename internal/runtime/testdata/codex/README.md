# `codex exec --json` fixtures

Inputs for `parseCodexStream` (`internal/runtime/codex_progress.go`) and its tests.

## Which fixture is real

| Fixture | Origin |
|---|---|
| `basic_run.ndjson` | **Live capture**, 2026-10-01, `@openai/codex@0.159.3` (`npx`), model `gpt-5.6-luna`, API-key login (`codex login --with-api-key`). Captured by `regen.sh`; the throwaway working directory was rewritten to `/sandbox/workspace/repo`, nothing else was edited. Reasoning items appear in roughly one capture in four; keep one that has it. |
| everything else | Hand-authored to the structs below. |

`regen.sh` re-captures `basic_run.ndjson` only. It reads `ARG CODEX_VERSION` from
`images/sandbox/Containerfile` (added by the image-pin PR of the codex runtime
stack) and falls back to the `CODEX_VERSION` environment variable, so until that
ARG exists run `CODEX_VERSION=0.152.1 ./regen.sh`.

## Hand-authored fixtures

| Fixture | Covers |
|---|---|
| `turn_failed.ndjson` | `turn.failed` after a failed command — the run is an error, with the bounded message. |
| `error_event.ndjson` | An `error` **item** (a warning/model-reroute) and a **top-level** `error`, both followed by `turn.completed`: neither fails the run. |
| `critical_error_only.ndjson` | A top-level `error` and then nothing: no terminal event, so the run is incomplete and the parked message is the reason. |
| `mcp_and_file_change.ndjson` | `file_change` (add/update/delete, and a failed patch), `mcp_tool_call` (success and error), `web_search`, `collab_tool_call`, `todo_list`, a `declined` command, and a multi-line command. |
| `malformed_line.ndjson` | Garbage, half-written and empty lines mid-stream: skipped, the run still completes. |
| `truncated.ndjson` | Killed mid-write — the last line stops mid-token and there is no terminal event. (The repo's `end-of-file-fixer` hook keeps a trailing newline after it; the line is still unparseable, which is what the fixture tests.) |
| `unknown_types.ndjson` | An unknown top-level event type and an unknown item type: skipped, the run still completes. |
| `second_turn_unfinished.ndjson` | A completed turn followed by a second `turn.started` that never finishes: the run is incomplete, not the first turn's success. |
| `collab_spawn.ndjson` | Three completed `spawn_agent` items, one **failed** one whose receiver is not a child, failed `wait` and `close_agent` items for the errored child inside a completed turn, and the parent usage of the 2026-09-29 paid probe. A hook-denied spawn emits no item; the failed one is hand-authored to pin the status filter. |

## Child rollout fixtures (`child_usage_*.jsonl`)

Codex **rollouts** (`$CODEX_HOME/sessions/**/rollout-*.jsonl`) for `foldCodexChildUsage`, not
`exec --json` streams, modelled on the 0.157.0 paid probe of 2026-09-29, with synthetic ids. The fold reads each rollout's last `turn_context` (`payload.model`) and last
`token_usage_record` (`payload.thread_token_usage`).

| Fixture | Covers |
|---|---|
| `child_usage_same_model.jsonl` | A child on the parent's model, `gpt-5.6-luna`, with two cumulative `token_count` lines (3000 then 7381 input) and the `token_usage_record` of its second response, whose thread total (7381) is the child's whole usage. |
| `child_usage_other_model.jsonl` | A child on `gpt-5.6-terra`, every normalized counter non-zero. |
| `child_usage_failed.jsonl` | A child whose model the provider rejected (404): `turn_context` names the model, `task_complete` carries `error`, and there is no `token_usage_record`, so it contributes nothing. |
| `child_usage_no_model.jsonl` | A child with a `token_usage_record` and no `turn_context`: its usage is bucketed under `unknown`. |
| `child_usage_forked.jsonl` | A forked child (hand-authored from codex 0.159.3's fork rules): the parent's `turn_context` (`gpt-5.6-luna`) and `token_count` copied ahead of the child's own `turn_context` (`gpt-5.6-terra`) and `token_usage_record`; its usage lands under `gpt-5.6-terra`. |

The tests lay the rollouts out under a temporary sessions tree as
`2026/09/29/rollout-2026-09-29T15-46-38-<thread id>.jsonl`, the name codex gives a rollout,
and the findings log beside the tree as `.security/findings.jsonl`. `spawn_admissions.jsonl` is
that log for a run whose spawn guard admitted three spawns and denied one, in the adapter's
`log_finding` shape, plus one `secret_redact_posttool` finding; `countCodexSpawnAdmissions`
counts the three `codex_spawn_guard_admit` lines by name.

## Event structs

Copied from `openai/codex` tag `rust-v0.152.1` and re-checked at `rust-v0.157.0` and `rust-v0.159.3`
(both files are byte-identical between the `rust-v0.157.0` and `rust-v0.159.3` tags),
`codex-rs/exec/src/exec_events.rs` (shapes) and
`codex-rs/exec/src/event_processor_with_jsonl_output.rs` (when each event is
emitted). Re-check both when `CODEX_VERSION` moves, and with them three facts the admission
cross-check (`codex_subagents.go`) rests on at `rust-v0.159.3`: a resume reaches the stream as a
`wait` item (`CollabAgentTool::ResumeAgent => CollabTool::Wait` in the same processor); in-process
delivery guarantees only TurnCompleted, the thread-state notifications and async AgentMessage
items, drops any other item notification (a collab item's `item.completed` included) when its
channel is full, and logs `dropping in-process server notification (queue full)` at warn from
`codex_app_server::in_process` (`codex-rs/app-server/src/in_process.rs`), which the runner scans
for, while exec re-emits at `turn/completed` the completed items whose `item.started` it saw
(`maybe_backfill_turn_completed_items`, `reconcile_unfinished_started_items`), only for a
non-ephemeral thread and only when its `thread/read` succeeds (`should_backfill_turn_completed_items`;
a failed read logs `thread/read failed while backfilling turn items for turn completion` at warn
from `codex_exec`, which the runner counts too), so a spawn that lost both notifications or whose
backfill failed is missing; and exec's default stderr filter is `error,opentelemetry_sdk=off,opentelemetry_otlp=off`
(`codex-rs/exec/src/lib.rs`, `EXEC_DEFAULT_LOG_FILTER`), which the launch command keeps as its
base.

Top-level `ThreadEvent` — serde `tag = "type"`, the variant payload flattened
next to it:

```
thread.started   {thread_id}
turn.started     {}
turn.completed   {usage}
turn.failed      {error{message}}
item.started     {item}
item.updated     {item}
item.completed   {item}
error            {message}
```

`usage` (`Usage`, all i64):

```
{input_tokens, cached_input_tokens, cache_write_input_tokens,
 output_tokens, reasoning_output_tokens}
```

`item` is `ThreadItem {id, #[serde(flatten)] details}` where `details` is an
internally tagged (`tag = "type"`, `rename_all = "snake_case"`) enum — so on the
wire the item type is a **sibling** of `id`, not nested under a `details` key:

```
{"id":"item_1","type":"command_execution","command":"…", …}
```

Item payloads:

```
agent_message     {text}
reasoning         {text}
command_execution {command, aggregated_output, exit_code: int|null,
                   status: in_progress|completed|failed|declined}
file_change       {changes: [{path, kind: add|delete|update}],
                   status: in_progress|completed|failed}
mcp_tool_call     {server, tool, arguments, result: {content[], _meta?,
                   structured_content}|null, error: {message}|null,
                   status: in_progress|completed|failed}
collab_tool_call  {tool: spawn_agent|send_input|wait|close_agent,
                   sender_thread_id, receiver_thread_ids[], prompt|null,
                   agents_states{}, status: in_progress|completed|failed}
web_search        {id, query, action{type: search|open_page|find_in_page, …}}
todo_list         {items: [{text, completed}]}
error             {message}
```

There is **no cost field and no model field** anywhere in the stream.

## Behaviour the structs alone do not show

Verified in `event_processor_with_jsonl_output.rs` at the same tag, and encoded
in `parseCodexStream`:

- **`turn.completed.usage` is cumulative for the thread**, not the delta for the
  turn that just ended (`usage_from_last_total()` reads the last
  `ThreadTokenUsageUpdated` total). Successive values replace each other;
  summing them double-counts. The parser also keeps a per-field high-water
  mark, so a snapshot that reports *less* than the one before it cannot lower
  the baseline and let the recovery be counted twice.
- **The usage categories are nested, not disjoint.** Following the OpenAI
  Responses API, `input_tokens` is the whole input *including*
  `cached_input_tokens` and `cache_write_input_tokens`, and `output_tokens` is
  the whole output *including* `reasoning_output_tokens`. Anthropic's
  convention — the one fullsend's `RunMetrics` and the renderer's total assume
  — is the opposite: cache and reasoning are separate from input and output.
  `codexUsage.counters()` subtracts the subsets so the five normalized
  counters sum to the tokens actually used. In `basic_run.ndjson` that is
  43,934; passed through unchanged it would render as ~87,500.
- **An `error` item is not a failure.** The processor emits one for config
  warnings, generic warnings, deprecation notices and model reroutes, and keeps
  the status `Running`.
- **A top-level `error` event is not terminal either.** It is parked as
  `last_critical_error` and reused as the message if the turn later fails; if
  the turn completes, it is discarded.
- **An interrupted turn emits neither `turn.completed` nor `turn.failed`** — the
  processor shuts down silently — so a stream with no terminal event is an
  incomplete, failed run, not a success.
- `file_change` is documented as "emitted only as a completed event", but the
  live capture shows `item.started` **and** `item.completed` for it. The parser
  maps only `item.completed`, so this costs nothing either way.
- `codex exec` has **no `--ask-for-approval` flag** in 0.152.1; the approval
  policy is a `-c approval_policy=never` override.
