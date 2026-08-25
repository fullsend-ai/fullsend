---
sidebar_label: Choose a Runtime
---

# Choose an agent runtime

> **Claude Code is the stable default.** The fleet agents have run on Claude Code in production for a long time; it is what a new installation gets unless you ask for something else. **pi is in its enablement (experimental) phase** — it works end to end for `triage`, `prioritize`, `code` and `fix`, has no sub-agent tool yet (`review`/`retro` run in a single context), and its fleet pilot is still in progress. Unless you are taking part in that pilot, keep the default.

This page explains what the choice means and where it is made. **You do not select anything on this page** — the selection happens in the next step, [Configuring GitHub](configuring-github.md), when `fullsend github setup` prompts for the runtime (press Enter for `claude`) or when you pass `--runtime`.

Fullsend supports multiple agent runtimes. A runtime is the program that runs inside the sandbox and drives the model — it owns the tool-use loop, hook wiring, and transcript format. The runner (fullsend) owns everything outside: sandbox lifecycle, credentials, metrics, and the verdict.

## Available runtimes

| Runtime | Status | Description | When to use |
|---------|--------|-------------|-------------|
| `claude` | **Stable (default)** | Claude Code on Vertex AI | Every production deployment — mature, full sub-agent support for `review`/`retro` |
| `pi` | Experimental (enablement phase) | [Pi](https://github.com/earendil-works/pi) — Claude on Vertex by default; any provider pi supports by model name (e.g. Gemini on Vertex with the same credentials) | Opt-in pilots only; no sub-agent tool yet, so `review`/`retro` run single-context; see [Runtimes](../../runtimes.md) for known constraints |

## When and how the runtime is selected

1. **Next step — Configuring GitHub.** `fullsend github setup <owner/repo>` asks which runtime to use when run from a terminal; press Enter to keep `claude`. Passing `--runtime` skips the prompt. The setup PR it opens records the choice in `.fullsend/config.yaml` and describes how to change it. Nothing runs on this page — continue with [Configuring GitHub](configuring-github.md).
2. **Later — changing it.** Edit `runtime:` in the repo's `.fullsend/config.yaml` (the setup PR shows the key), or re-run `fullsend github setup <owner/repo> --runtime <claude|pi>`. Fleets managed through `repos.yaml` set `defaults.runtime` (or a per-entry `runtime`) — `fullsend repos set-default defaults.runtime pi` — and run `fullsend repos install`; see [fullsend repos](../../cli/repos.md).
3. **Per run — trying without changing the repo.** `fullsend run --runtime pi --model google-vertex/gemini-2.5-flash`, or the `FULLSEND_RUNTIME` / `FULLSEND_MODEL` / `FULLSEND_EFFORT` environment variables (flag beats environment beats config). In CI the same names work as repository variables. Reference: [fullsend run](../../cli/run.md) and [Runtimes — selecting and overriding](../../runtimes.md#selecting-a-runtime-and-model).

## Where to see what ran

After a run completes, the selected runtime and model appear in several places:

- **Run plan block** — `Runtime: <name> (from <source>)` printed at the start of every `fullsend run`
- **Status comment** — the terminal status comment on the issue/PR includes a footer with runtime, model, effort, and cost
- **metrics.json** — `runtime`, `requested_runtime`, `runtime_source`, `requested_model`, and `override_source` fields record what was selected and why
- **stderr** — `runtime: selected "<name>" from <source>` for script consumers

## Next steps

- [Configuring GitHub](configuring-github.md) to set up your repo
- [Runtimes](../../runtimes.md) for the full runtime reference, including model override precedence and the capability table
