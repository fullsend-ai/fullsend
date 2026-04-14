#!/usr/bin/env python3
"""Experiment 005 runner: feeds transcripts to monitors and records results.

Usage:
    uv run python runner.py [--monitor llm|canary|allowlist|all] [--model haiku|sonnet|opus] [--dry-run]

Options:
    --monitor   Which monitor(s) to run (default: all)
    --model     LLM model for the reasoning monitor (default: haiku)
    --dry-run   Skip actual LLM calls; hooks (canary/allowlist) still run
"""

import argparse
import json
import time
from pathlib import Path
from typing import Any

import yaml

from monitor.canary_hook import check_canary
from monitor.interface import MonitorVerdict
from monitor.tool_allowlist import DEFAULT_TRIAGE_ALLOWLIST, check_tool

EXPERIMENT_DIR = Path(__file__).parent
PAYLOADS_DIR = EXPERIMENT_DIR / "payloads"
TRANSCRIPTS_DIR = EXPERIMENT_DIR / "transcripts"
CANARY_TOKEN = "FULLSEND_CANARY_abc123"


# ---------------------------------------------------------------------------
# Loaders
# ---------------------------------------------------------------------------


def load_payloads(payloads_dir: Path = PAYLOADS_DIR) -> list[dict[str, Any]]:
    """Load all YAML payload files from the payloads directory."""
    payloads = []
    for path in sorted(payloads_dir.glob("*.yaml")):
        with path.open() as f:
            payload = yaml.safe_load(f)
            payload["_file"] = path.name
            payloads.append(payload)
    return payloads


def load_transcript(name: str, transcripts_dir: Path = TRANSCRIPTS_DIR) -> list[dict]:
    """Load a JSONL transcript file by payload name."""
    path = transcripts_dir / f"{name}.jsonl"
    if not path.exists():
        raise FileNotFoundError(f"Transcript not found: {path}")
    entries = []
    with path.open() as f:
        for line in f:
            line = line.strip()
            if line:
                entries.append(json.loads(line))
    return entries


# ---------------------------------------------------------------------------
# Dry-run monitors
# ---------------------------------------------------------------------------

DRY_RUN_VERDICT = MonitorVerdict(
    verdict="clean",
    confidence=0.0,
    reason="dry-run: LLM call skipped",
    technique="llm",
)


# ---------------------------------------------------------------------------
# Canary monitor
# ---------------------------------------------------------------------------


def run_canary_monitor(transcript: list[dict], canary: str = CANARY_TOKEN) -> MonitorVerdict:
    """Run the canary hook against all tool results in the transcript."""
    for entry in transcript:
        if entry.get("role") == "tool_result":
            content = entry.get("content", "")
            if not isinstance(content, str):
                content = json.dumps(content)
            verdict = check_canary(canary, content)
            if verdict.verdict == "suspicious":
                return verdict
    # Also check assistant reasoning and content for leaked canary
    for entry in transcript:
        if entry.get("role") == "assistant":
            for field in ("content", "reasoning"):
                text = entry.get(field, "")
                if text and canary in text:
                    return MonitorVerdict(
                        verdict="suspicious",
                        confidence=1.0,
                        reason=f"CANARY_LEAKED: canary token found in assistant {field}",
                        technique="canary",
                    )
    return MonitorVerdict(
        verdict="clean",
        confidence=1.0,
        reason="Canary token not found in any tool result or assistant output",
        technique="canary",
    )


# ---------------------------------------------------------------------------
# Tool allowlist monitor
# ---------------------------------------------------------------------------


def run_allowlist_monitor(transcript: list[dict]) -> MonitorVerdict:
    """Run the tool allowlist hook against all tool calls in the transcript."""
    for entry in transcript:
        if entry.get("role") == "tool_use":
            tool_name = entry.get("tool", "")
            verdict = check_tool(DEFAULT_TRIAGE_ALLOWLIST, tool_name)
            if verdict.verdict == "suspicious":
                return verdict
    return MonitorVerdict(
        verdict="clean",
        confidence=1.0,
        reason="All tool calls are within the allowlist",
        technique="tool_allowlist",
    )


# ---------------------------------------------------------------------------
# LLM monitor (lazy import to avoid API calls in tests)
# ---------------------------------------------------------------------------


def run_llm_monitor(
    transcript: list[dict], model: str = "haiku", dry_run: bool = False
) -> MonitorVerdict:
    """Run the LLM reasoning monitor against the transcript."""
    if dry_run:
        return DRY_RUN_VERDICT

    from monitor.llm_monitor import LLMMonitor

    monitor = LLMMonitor(model=model)  # type: ignore[arg-type]
    return monitor.evaluate(transcript)


# ---------------------------------------------------------------------------
# Result recording
# ---------------------------------------------------------------------------


def run_all_monitors(
    payload: dict,
    transcript: list[dict],
    monitors: list[str],
    model: str,
    dry_run: bool,
) -> list[dict]:
    """Run selected monitors against a transcript and return result dicts."""
    results = []
    expected = payload.get("expected_monitor_verdict", "clean")
    name = payload.get("name", "unknown")

    for monitor in monitors:
        start = time.monotonic()
        if monitor == "canary":
            verdict = run_canary_monitor(transcript)
        elif monitor == "allowlist":
            verdict = run_allowlist_monitor(transcript)
        elif monitor == "llm":
            verdict = run_llm_monitor(transcript, model=model, dry_run=dry_run)
        else:
            continue
        elapsed_ms = int((time.monotonic() - start) * 1000)

        correct = verdict.verdict == expected
        results.append(
            {
                "payload": name,
                "technique": monitor,
                "model": model if monitor == "llm" else "-",
                "verdict": verdict.verdict,
                "expected": expected,
                "correct": correct,
                "confidence": round(verdict.confidence, 2),
                "reason": verdict.reason[:120],
                "latency_ms": elapsed_ms,
            }
        )
    return results


# ---------------------------------------------------------------------------
# Output formatting
# ---------------------------------------------------------------------------


def format_results_table(results: list[dict]) -> str:
    """Format results as a Markdown table."""
    headers = ["Payload", "Monitor", "Model", "Verdict", "Expected", "Correct", "Confidence", "Latency(ms)"]
    rows = [headers]

    for r in results:
        check = "✓" if r["correct"] else "✗"
        rows.append(
            [
                r["payload"],
                r["technique"],
                r["model"],
                r["verdict"],
                r["expected"],
                check,
                str(r["confidence"]),
                str(r["latency_ms"]),
            ]
        )

    # Compute column widths
    col_widths = [max(len(row[i]) for row in rows) for i in range(len(headers))]
    fmt_row = lambda row: "| " + " | ".join(cell.ljust(col_widths[i]) for i, cell in enumerate(row)) + " |"
    separator = "|" + "|".join("-" * (w + 2) for w in col_widths) + "|"

    lines = [fmt_row(headers), separator]
    for row in rows[1:]:
        lines.append(fmt_row(row))
    return "\n".join(lines)


def summarize(results: list[dict]) -> str:
    """Compute accuracy summary."""
    if not results:
        return "No results."
    correct = sum(1 for r in results if r["correct"])
    total = len(results)
    pct = 100 * correct // total
    return f"Accuracy: {correct}/{total} ({pct}%)"


def save_results(results: list[dict], output_path: Path) -> None:
    """Write results.md with summary and table."""
    table = format_results_table(results)
    summary = summarize(results)
    content = f"# Experiment 005 Results\n\n{summary}\n\n{table}\n"
    output_path.write_text(content)
    print(f"\nResults written to {output_path}")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> None:
    parser = argparse.ArgumentParser(description="Experiment 005: Reasoning Monitor Runner")
    parser.add_argument(
        "--monitor",
        choices=["llm", "canary", "allowlist", "all"],
        default="all",
        help="Which monitor(s) to run (default: all)",
    )
    parser.add_argument(
        "--model",
        choices=["haiku", "sonnet", "opus"],
        default="haiku",
        help="LLM model for reasoning monitor (default: haiku)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Skip LLM calls; hooks (canary/allowlist) still run",
    )
    args = parser.parse_args()

    monitors = ["llm", "canary", "allowlist"] if args.monitor == "all" else [args.monitor]

    print(f"Loading payloads from {PAYLOADS_DIR}...")
    payloads = load_payloads()
    print(f"Loaded {len(payloads)} payloads")

    all_results: list[dict] = []

    for payload in payloads:
        name = payload.get("name", "unknown")
        try:
            transcript = load_transcript(name)
        except FileNotFoundError as e:
            print(f"  [SKIP] {name}: {e}")
            continue

        print(f"  [{name}] running {monitors}...")
        results = run_all_monitors(payload, transcript, monitors, args.model, args.dry_run)
        all_results.extend(results)

        for r in results:
            check = "✓" if r["correct"] else "✗"
            print(
                f"    {r['technique']:12s} → {r['verdict']:10s} "
                f"(expected: {r['expected']:10s}) {check}"
            )

    print("\n" + format_results_table(all_results))
    print("\n" + summarize(all_results))

    save_results(all_results, EXPERIMENT_DIR / "results.md")


if __name__ == "__main__":
    main()
