# `codex exec --json` fixtures

Inputs for `parseCodexStream` (`internal/runtime/codex_progress.go`) and its tests.

## Which fixture is real

| Fixture | Origin |
|---|---|
| `basic_run.ndjson` | **Live capture**, 2026-09-02, `@openai/codex@0.152.1` (`npx`), model `gpt-5.6-luna`, host login (`~/.codex/auth.json`). Captured by `regen.sh`; the throwaway working directory was rewritten to `/sandbox/workspace/repo`, nothing else was edited. |
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

## Event structs

Copied from `openai/codex` tag `rust-v0.152.1`, `codex-rs/exec/src/exec_events.rs`
(shapes) and `codex-rs/exec/src/event_processor_with_jsonl_output.rs` (when each
event is emitted). Re-check both when `CODEX_VERSION` moves.

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
  41,615; passed through unchanged it would render as ~83,000.
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
