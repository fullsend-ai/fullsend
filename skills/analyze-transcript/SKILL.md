---
name: analyze-transcript
description: >
  Analyze fullsend agent run transcripts from GitHub Actions artifacts.
  Use when the user wants to inspect what an agent did during a run,
  debug failures, check tool usage, or search transcript content.
  Handles downloading artifacts, finding JSONL files, and running
  the analyzer script.
allowed-tools:
  - Bash(gh run view *)
  - Bash(gh run download *)
  - Bash(mkdir -p .transcripts/*)
  - Bash(mkdir -p ".transcripts/*")
  - Bash(find .transcripts/run-*)
  - Bash(find ".transcripts/run-*")
  - Bash(python3 *analyze-transcript.py *)
---

# Analyze Agent Transcript

Download and analyze fullsend agent JSONL transcripts from GitHub Actions runs.

## Prerequisites

- `gh` CLI authenticated with access to the target repository
- Python 3.8+

## Rules

- **Never auto-dispatch subagents.** Try `search`, `errors`, or
  `conversation --lines` first. Only _suggest_ a subagent if targeted
  commands aren't enough — never spawn one silently.
- **Always use relative paths.** Pass `.transcripts/run-<id>/...` to the
  script, not absolute paths. Absolute paths trigger permission prompts.
- **No shell variables.** Never assign paths to variables or export them.
  No `DLDIR=...`, `TRANSCRIPT=...`, `SANDBOX_LOG=...`, `BASE_DIR=...`,
  `export ...`. Inline the literal path in every command. This applies to
  the download dir, transcript paths, sandbox log paths, and the script
  path. Variables cause commands to drift from the allowed-tools patterns
  and trigger unnecessary permission prompts.
- **Limit output.** Use `| head -N`, `| tail -N`, or `--lines` to keep
  transcript output short. Never dump a full conversation into the main
  context.
- **Plain `find` only.** Never use `-exec`, `-ls`, or `-printf` with
  `find`. Use `find ... -print` (the default). If you need file sizes,
  run `ls -lh` on the directory instead.

## Script location

The analyzer script lives alongside this skill:

```
skills/analyze-transcript/analyze-transcript.py
```

Resolve the path from the skill's base directory. If this skill was loaded
from a base directory, the script is at `<base-dir>/analyze-transcript.py`.

## Question routing

Route the user's question to the right subcommand before reaching for full
conversation output:

| User asks about | Start with |
|---|---|
| network / POST / REST / blocked / denied | `network`, `netsearch` |
| error / failed / broke / crash | `errors` |
| why / decided / reasoning | `search "<keyword>"`, then `conversation --lines` around hits |
| what changed / commit / diff | `search "git diff"`, `search "git commit"` |
| how long / duration / timeout | `summary` |
| token / cost | `summary` |
| everything / full picture | `audit` |

## Workflow

### 1. Get the run

The user provides a GitHub Actions run URL. Extract owner, repo, and run ID.

URL formats:
- `https://github.com/OWNER/REPO/actions/runs/RUN_ID`
- `https://github.com/OWNER/REPO/actions/runs/RUN_ID/job/JOB_ID`
- `https://github.com/OWNER/REPO/actions/runs/RUN_ID/attempts/N`

### 2. Check run status

```bash
gh run view <run-id> --repo OWNER/REPO --json status,conclusion,createdAt,updatedAt
```

If still running, tell the user and offer to watch it.

### 3. Download artifacts

```bash
mkdir -p .transcripts/run-<run-id>
gh run download <run-id> --repo OWNER/REPO --dir .transcripts/run-<run-id>
```

The `.transcripts/` directory is gitignored. Using the working directory avoids
permission prompts for `/tmp` writes.

### 4. Find artifacts

Downloaded artifacts have this structure:

```
.transcripts/run-<run-id>/fullsend-<agent>/
  agent-<type>-<id>/
    logs/
      openshell-sandbox.log    # OCSF network/process/sandbox events
      openshell-gateway.log    # Gateway-side events
    iteration-1/
      transcripts/
        <agent>-<session-id>.jsonl   # Main agent transcript
        <agent>-agent-*.jsonl        # Subagent transcripts
```

Find transcripts and logs:

```bash
find .transcripts/run-<run-id> -name "*.jsonl" -type f -print
find .transcripts/run-<run-id> -name "openshell-sandbox.log" -type f -print
```

Each JSONL file is one agent session (main agent or subagent). The sandbox
log contains all OCSF network events for the run. If you need file sizes,
use `ls -lh` on the directory.

### 5. No transcript found?

If `find` returns no `.jsonl` transcript files, the run likely failed before
the agent started (security scan, infra failure, OPA denial). Fall back to:

1. Check for non-transcript files in the artifact dir:
   - `run-summary.json` — structured run metadata
   - `run-telemetry.jsonl` — OTLP telemetry (not a Claude transcript)
2. Check sandbox logs for early failures:
   ```bash
   python3 <base-dir>/analyze-transcript.py netsearch "DENIED" <sandbox-log>
   ```
3. Pull GitHub Actions logs directly:
   ```bash
   gh run view <run-id> --repo OWNER/REPO --log-failed
   ```

**Do not confuse `run-telemetry.jsonl` with Claude transcripts.** Telemetry
files contain OTLP spans, not conversation messages. The script will detect
this and warn you.

### 6. Run analysis

**Quick audit (summary + errors + tools in one shot):**
```bash
python3 <base-dir>/analyze-transcript.py audit <path-to-jsonl>
```

**Summary only:**
```bash
python3 <base-dir>/analyze-transcript.py summary <path-to-jsonl>
```
Shows: agent type, model, duration, message count, token usage, tool call
breakdown, and stop reasons.

### 7. Deeper analysis based on user questions

Use the appropriate subcommand:

**Full conversation flow (alias: `conv`):**
```bash
python3 <base-dir>/analyze-transcript.py conversation <path> | head -100
```
Shows assistant text, tool calls, and results with line numbers. Use
`| head -N` for the start or `| tail -N` for the end of large transcripts.
Use `--max-width 0` for full untruncated output.

**Note:** System prompts and pre-conversation context are not included in
`conversation` output. If the user asks "why did the agent do X", search for
decision-relevant keywords first, then show surrounding conversation lines.

**Tool usage breakdown:**
```bash
python3 <base-dir>/analyze-transcript.py tools <path>
```

**Errors and failures only:**
```bash
python3 <base-dir>/analyze-transcript.py errors <path>
```
Finds tool errors, permission denials, failed commands, and error mentions.

**Search for specific content:**
```bash
python3 <base-dir>/analyze-transcript.py search "yarn install" <path>
```
Regex search across all tool results and assistant text.

**Restrict to line range (place after subcommand; transcript subcommands only):**
```bash
python3 <base-dir>/analyze-transcript.py conversation <path> --lines 50-100
```

**JSON output (available on `summary`, `tools`, and `network`):**
```bash
python3 <base-dir>/analyze-transcript.py summary <path> --json
```

### 8. Sandbox network analysis

The `openshell-sandbox.log` contains OCSF events for all network connections,
HTTP requests, process launches, and policy decisions made inside the sandbox.

**Network summary (alias: `net`):**
```bash
python3 <base-dir>/analyze-transcript.py network <sandbox-log>
```
Shows: duration, denied connections, destination hosts, OPA policies hit, and
event type breakdown.

**Network summary with HTTP request list:**
```bash
python3 <base-dir>/analyze-transcript.py network <sandbox-log> --http
```
Appends a chronological list of every HTTP request with relative timestamps.

**Filter HTTP requests by method or host:**
```bash
python3 <base-dir>/analyze-transcript.py network <sandbox-log> --http --method POST
python3 <base-dir>/analyze-transcript.py network <sandbox-log> --http --method POST,PUT,PATCH,DELETE
python3 <base-dir>/analyze-transcript.py network <sandbox-log> --http --host github.com
python3 <base-dir>/analyze-transcript.py network <sandbox-log> --http --method POST,PUT,PATCH --host github.com
```

**Search network logs:**
```bash
python3 <base-dir>/analyze-transcript.py network-search "DENIED" <sandbox-log>
python3 <base-dir>/analyze-transcript.py netsearch "github" <sandbox-log>
```
Regex search across all OCSF event lines. Matches against raw log text,
destination hosts, and HTTP URLs.

## Full audit workflow

When the user wants the full picture, run this sequence:

1. `audit <transcript>` — summary + errors + tools in one pass
2. `network <sandbox-log>` — denied connections, hosts, policies
3. `conversation <transcript> | head -80` — how the run started
4. `conversation <transcript> | tail -50` — how the run ended

## Common analysis patterns

- **Why did a run fail?** Start with `errors`, then `conversation` around the
  error lines to see what the agent tried.
- **Auth token expiry?** Search for `invalid_grant` or `stale to sign-in`:
  `search "invalid_grant|stale" <path>`. Common when runs exceed the ID token
  lifetime.
- **What did the agent change?** Search for `git diff` or `git commit` in the
  transcript: `search "git diff" <path>`
- **How did the run end?** Use `conversation <path> | tail -30` to see the
  final actions. Useful for spotting token expiry, timeouts, or incomplete work.
- **How long did yarn/npm install take?** Search for the install command and
  check timestamps.
- **Did the agent hit the timeout?** Check `summary` duration vs the harness
  `timeout_minutes`.
- **Token cost?** `summary` shows input/output/cache token counts.
- **Was a network request blocked?** `network <sandbox-log>` shows all denials
  at the top. Use `netsearch "DENIED"` for raw lines.
- **What endpoints did the agent talk to?** `network <sandbox-log>` lists hosts
  by frequency. Add `--http` for the full request log.
- **Which OPA policy allowed/denied traffic?** `network` shows policy hit
  counts. `netsearch "policy:<name>"` filters to a specific policy.
- **Did the agent make write requests to GitHub?** Filter POST/PUT/PATCH to
  github.com: `network <sandbox-log> --http --method POST,PUT,PATCH --host github.com`

## Notes

- Download artifacts to `.transcripts/` in the working directory (gitignored).
- The script is stdlib-only Python — no pip install needed.
- The `errors` command shows definitive errors (is_error flag, exit codes,
  tracebacks) separately from keyword mentions. For targeted searching, prefer
  `search "pattern"` over `errors` — it produces fewer false positives.
- For visual interactive replay, use the `replay-session` skill instead.

## Related skills

- If you need to find the GitHub Actions run ID for an agent given an issue or PR number, use the `finding-agent-runs` skill if available.
- For visual interactive replay of a session, use the `replay-session` skill if available.
