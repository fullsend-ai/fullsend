---
name: analyze-transcript
description: >
  Analyze fullsend agent run transcripts from GitHub Actions artifacts.
  Use when the user wants to inspect what an agent did during a run,
  debug failures, check tool usage, or search transcript content.
  Handles downloading artifacts, finding JSONL files, and running
  the analyzer script.
---

# Analyze Agent Transcript

Download and analyze fullsend agent JSONL transcripts from GitHub Actions runs.

## Prerequisites

- `gh` CLI authenticated with access to the target repository
- Python 3.8+

## Script location

The analyzer script lives alongside this skill:

```
skills/analyze-transcript/analyze-transcript.py
```

Resolve the absolute path from the skill's base directory. If this skill was
loaded from a base directory, the script is at `<base-dir>/analyze-transcript.py`.

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
DLDIR=".transcripts/run-<run-id>"
mkdir -p "$DLDIR"
gh run download <run-id> --repo OWNER/REPO --dir "$DLDIR"
```

The `.transcripts/` directory is gitignored. Using the working directory avoids
permission prompts for `/tmp` writes.

### 4. Find artifacts

Downloaded artifacts have this structure:

```
<DLDIR>/fullsend-<agent>/
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
find "$DLDIR" -name "*.jsonl" -type f
find "$DLDIR" -name "openshell-sandbox.log" -type f
```

List files with sizes. Each JSONL file is one agent session (main agent or
subagent). The sandbox log contains all OCSF network events for the run.

### 5. Run summary on each transcript

```bash
python3 <base-dir>/analyze-transcript.py summary <path-to-jsonl>
```

This gives: agent type, model, duration, message count, token usage, tool call
breakdown, and stop reasons.

### 6. Deeper analysis based on user questions

Use the appropriate subcommand:

**Full conversation flow (alias: `conv`):**
```bash
python3 <base-dir>/analyze-transcript.py conversation <path> | head -100
```
Shows assistant text, tool calls, and results with line numbers. Use
`| head -N` for the start or `| tail -N` for the end of large transcripts.
Use `--max-width 0` for full untruncated output.

**Context window caution:** Transcript output can be very large. When analyzing
transcripts from within an agent session, dispatch a subagent to run analysis
commands and summarize findings rather than running them in your main context.
Use `| head -N` or `--lines` to limit output when running directly.

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

### 7. Sandbox network analysis

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

**Search network logs:**
```bash
python3 <base-dir>/analyze-transcript.py network-search "DENIED" <sandbox-log>
python3 <base-dir>/analyze-transcript.py netsearch "github" <sandbox-log>
```
Regex search across all OCSF event lines. Matches against raw log text,
destination hosts, and HTTP URLs.

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
- **Token cost?** `summary` shows input/output/cache token counts and a `Cost: ~$X.XXXX` line with per-component breakdown (input, output, cache-write, cache-read). Add `--json` to get a `cost_usd` key with `total` and per-component values. Cost is approximate (Anthropic list prices; actual Vertex AI billing may differ). Omitted for unrecognised models.
- **Was a network request blocked?** `network <sandbox-log>` shows all denials
  at the top. Use `netsearch "DENIED"` for raw lines.
- **What endpoints did the agent talk to?** `network <sandbox-log>` lists hosts
  by frequency. Add `--http` for the full request log.
- **Which OPA policy allowed/denied traffic?** `network` shows policy hit
  counts. `netsearch "policy:<name>"` filters to a specific policy.

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
