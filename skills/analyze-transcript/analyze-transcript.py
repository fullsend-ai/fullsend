#!/usr/bin/env python3
"""Analyze fullsend agent JSONL transcripts."""

import argparse
import json
import re
import signal
import sys
from collections import Counter
from datetime import datetime
from typing import TypedDict
from urllib.parse import urlsplit

# Prevent BrokenPipeError when output is piped through head/tail.
signal.signal(signal.SIGPIPE, signal.SIG_DFL)


def parse_lines(path, line_range=None):
    """Read JSONL file and yield (line_number, parsed_object) tuples."""
    if path == "-":
        yield from _parse_source(sys.stdin, line_range)
    else:
        with open(path, errors="replace") as f:
            yield from _parse_source(f, line_range)


def _parse_source(source, line_range):
    for i, raw in enumerate(source):
        if line_range:
            lo, hi = line_range
            if i < lo:
                continue
            if hi is not None and i > hi:
                break
        raw = raw.strip()
        if not raw:
            continue
        try:
            obj = json.loads(raw)
        except json.JSONDecodeError:
            continue
        if not isinstance(obj, dict):
            continue
        yield i, obj


def parse_line_range(spec):
    """Parse 'N-M', 'N-', or 'N' into (lo, hi|None)."""
    if not spec:
        return None
    if "-" in spec:
        parts = spec.split("-", 1)
        lo = int(parts[0]) if parts[0] else 0
        hi = int(parts[1]) if parts[1] else None
        return (lo, hi)
    n = int(spec)
    return (n, n)


def truncate(text, max_width):
    if max_width and len(text) > max_width:
        return text[:max_width] + "..."
    return text


def extract_content_blocks(msg):
    """Yield (block_type, block_data) from a message's content."""
    content = msg.get("content", "")
    if isinstance(content, str):
        if content.strip():
            yield ("text", content)
        return
    if isinstance(content, list):
        for block in content:
            if isinstance(block, str):
                if block.strip():
                    yield ("text", block)
            elif isinstance(block, dict):
                yield (block.get("type", "unknown"), block)


def get_tool_result_text(block):
    """Extract text from a tool_result block."""
    c = block.get("content", "")
    if isinstance(c, str):
        return c
    if isinstance(c, list):
        parts = []
        for sub in c:
            if isinstance(sub, dict) and sub.get("type") == "text":
                parts.append(sub.get("text", ""))
        return "\n".join(parts)
    return ""


def detect_file_type(path):
    """Check first few lines to detect file type. Returns None if it looks like
    a valid transcript, or a warning string if it's something else."""
    if path == "-":
        return None
    try:
        with open(path) as f:
            for _ in range(5):
                line = f.readline()
                if not line:
                    break
                line = line.strip()
                if not line:
                    continue
                try:
                    obj = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if not isinstance(obj, dict):
                    continue
                if "resourceSpans" in obj or "scopeSpans" in obj:
                    return (
                        "This looks like OTLP telemetry data, not a Claude transcript. "
                        "Look for a file named <agent>-<session-id>.jsonl instead."
                    )
                if obj.get("type") in ("assistant", "user", "agent-setting"):
                    return None
    except (OSError, UnicodeDecodeError):
        pass
    return None


def iter_messages(path, line_range=None):
    """Yield (line_num, role, msg_obj, raw_obj) for message-type lines."""
    for i, obj in parse_lines(path, line_range):
        obj_type = obj.get("type", "")
        if obj_type in ("assistant", "user"):
            msg = obj.get("message", {})
            role = msg.get("role", obj_type)
            yield i, role, msg, obj
        elif obj_type == "agent-setting":
            yield i, "meta", obj, obj
        elif obj_type == "queue-operation":
            yield i, "queue", obj, obj
        elif obj_type == "last-prompt":
            yield i, "meta", obj, obj


# --- Subcommands ---


def _check_file_type(path):
    warning = detect_file_type(path)
    if warning:
        print(f"Warning: {warning}", file=sys.stderr)
        sys.exit(1)


def _accumulate_stats(path, line_range=None, messages=None):
    """Single-pass accumulation of model/session/token/duration/tool/stop-reason stats."""
    models = set()
    session_ids = set()
    timestamps = []
    tool_counts = Counter()
    msg_counts = Counter()
    total_input_tokens = 0
    total_output_tokens = 0
    total_cache_read = 0
    total_cache_create = 0
    stop_reasons = Counter()
    agent_setting = None

    source = messages if messages is not None else iter_messages(path, line_range)
    for _i, role, msg, raw in source:
        if role == "meta":
            if raw.get("type") == "agent-setting":
                agent_setting = raw.get("agentSetting")
                sid = raw.get("sessionId")
                if sid:
                    session_ids.add(sid)
            continue
        if role == "queue":
            ts = raw.get("timestamp")
            if ts:
                timestamps.append(ts)
            continue

        msg_counts[role] += 1
        ts = raw.get("timestamp")
        if ts:
            timestamps.append(ts)

        if role == "assistant":
            model = msg.get("model")
            if model:
                models.add(model)
            usage = msg.get("usage", {})
            total_input_tokens += usage.get("input_tokens", 0)
            total_output_tokens += usage.get("output_tokens", 0)
            total_cache_read += usage.get("cache_read_input_tokens", 0)
            total_cache_create += usage.get("cache_creation_input_tokens", 0)
            sr = msg.get("stop_reason")
            if sr:
                stop_reasons[sr] += 1

        for btype, block in extract_content_blocks(msg):
            if role == "assistant" and btype == "tool_use":
                tool_counts[block.get("name", "unknown")] += 1

    duration = None
    if len(timestamps) >= 2:
        try:
            ts_sorted = sorted(timestamps)
            t0 = datetime.fromisoformat(ts_sorted[0].replace("Z", "+00:00"))
            t1 = datetime.fromisoformat(ts_sorted[-1].replace("Z", "+00:00"))
            duration = (t1 - t0).total_seconds()
        except (ValueError, TypeError):
            pass

    return {
        "agent": agent_setting,
        "session_ids": sorted(session_ids),
        "models": sorted(models),
        "messages": msg_counts,
        "tokens": {
            "input": total_input_tokens,
            "output": total_output_tokens,
            "cache_read": total_cache_read,
            "cache_create": total_cache_create,
        },
        "duration_seconds": duration,
        "tool_calls": tool_counts,
        "stop_reasons": stop_reasons,
    }


def _print_stats(s):
    """Print the shared summary section from an _accumulate_stats result."""
    print(f"Agent:      {s['agent'] or 'unknown'}")
    print(f"Session:    {', '.join(s['session_ids']) or 'unknown'}")
    print(f"Model:      {', '.join(s['models']) or 'unknown'}")
    duration = s["duration_seconds"]
    if duration is not None:
        mins, secs = divmod(duration, 60)
        print(f"Duration:   {int(mins)}m {secs:.1f}s")
    msg_counts = s["messages"]
    msg_parts = ", ".join(f"{v} {k}" for k, v in msg_counts.items())
    print(f"Messages:   {sum(msg_counts.values())} ({msg_parts})")
    t = s["tokens"]
    print(
        f"Tokens:     {t['input']} in / "
        f"{t['output']} out / "
        f"{t['cache_read']} cache-read / "
        f"{t['cache_create']} cache-create"
    )
    tool_counts = s["tool_calls"]
    if tool_counts:
        print()
        print("Tool calls:")
        for name, count in tool_counts.most_common():
            print(f"  {name:30s} {count}")
    stop_reasons = s["stop_reasons"]
    if stop_reasons:
        print(f"\nStop reasons: {', '.join(f'{k}={v}' for k, v in stop_reasons.items())}")


def cmd_summary(args):
    _check_file_type(args.file)
    s = _accumulate_stats(args.file, args.line_range)

    if args.json_output:
        out = dict(s)
        out["messages"] = dict(out["messages"])
        out["tool_calls"] = dict(out["tool_calls"].most_common())
        out["stop_reasons"] = dict(out["stop_reasons"])
        print(json.dumps(out, indent=2))
        return

    _print_stats(s)


def cmd_conversation(args):
    _check_file_type(args.file)
    max_w = args.max_width

    for i, role, msg, _raw in iter_messages(args.file, args.line_range):
        if role in ("meta", "queue"):
            continue

        for btype, block in extract_content_blocks(msg):
            if role == "assistant":
                if btype == "text":
                    text = block if isinstance(block, str) else block.get("text", "")
                    text = text.strip()
                    if text:
                        print(f"L{i} ASSISTANT: {truncate(text, max_w)}")
                        print()
                elif btype == "tool_use":
                    name = block.get("name", "?")
                    inp = json.dumps(block.get("input", {}))
                    print(f"L{i} TOOL CALL: {name}  {truncate(inp, max_w)}")
                    print()
            elif role == "user":
                if btype == "text":
                    text = block if isinstance(block, str) else block.get("text", "")
                    text = text.strip()
                    if text:
                        print(f"L{i} USER: {truncate(text, max_w)}")
                        print()
                elif btype == "tool_result":
                    result_text = get_tool_result_text(block)
                    if result_text.strip():
                        print(f"L{i} RESULT: {truncate(result_text.strip(), max_w)}")
                        print()


class ToolStats(TypedDict):
    count: int
    lines: list[int]


def cmd_tools(args):
    _check_file_type(args.file)
    tool_data: dict[str, ToolStats] = {}

    for i, role, msg, _raw in iter_messages(args.file, args.line_range):
        if role != "assistant":
            continue
        for btype, block in extract_content_blocks(msg):
            if btype == "tool_use":
                name = block.get("name", "unknown")
                if name not in tool_data:
                    tool_data[name] = {"count": 0, "lines": []}
                tool_data[name]["count"] += 1
                tool_data[name]["lines"].append(i)

    if args.json_output:
        print(json.dumps(tool_data, indent=2))
        return

    if not tool_data:
        print("No tool calls found.")
        return

    sorted_tools = sorted(tool_data.items(), key=lambda x: -x[1]["count"])
    print(f"{'Tool':<30s} {'Count':>5s}  Lines")
    print("-" * 70)
    for name, data in sorted_tools:
        lines_str = ", ".join(str(ln) for ln in data["lines"][:10])
        if len(data["lines"]) > 10:
            lines_str += f" ... (+{len(data['lines']) - 10} more)"
        print(f"{name:<30s} {data['count']:>5d}  {lines_str}")


_ASSISTANT_ERROR_KEYWORDS = ["api error", "permission denied", "eacces", "fatal error"]


def _check_block_error(role, btype, block, line, max_w, errors, mentions):
    """Classify a single content block and append to errors/mentions if applicable."""
    if btype == "tool_result":
        if _is_error_result(block):
            text = get_tool_result_text(block)
            errors.append((line, truncate(text.strip(), max_w)))
        else:
            text = get_tool_result_text(block)
            if _RESULT_ERROR_PATTERNS.search(text):
                errors.append((line, truncate(text.strip(), max_w)))
    elif role == "user" and btype == "text":
        text = block if isinstance(block, str) else block.get("text", "")
        if "<error>" in text:
            errors.append((line, truncate(text.strip(), max_w)))
    elif role == "assistant" and btype == "text":
        text = block if isinstance(block, str) else block.get("text", "")
        lower = text.lower()
        if any(kw in lower for kw in _ASSISTANT_ERROR_KEYWORDS):
            mentions.append((line, truncate(text.strip(), max_w)))


def _collect_errors(file, max_w, line_range=None, messages=None):
    """Shared error collection used by cmd_errors."""
    errors = []
    mentions = []

    source = messages if messages is not None else iter_messages(file, line_range)
    for i, role, msg, _raw in source:
        for btype, block in extract_content_blocks(msg):
            _check_block_error(role, btype, block, i, max_w, errors, mentions)

    return errors, mentions


def cmd_errors(args):
    _check_file_type(args.file)
    errors, mentions = _collect_errors(args.file, args.max_width, args.line_range)

    if not errors and not mentions:
        print("No errors found.")
        return

    for line, text in errors:
        print(f"L{line} ERROR: {text}")
        print()
    if mentions:
        if errors:
            print("---")
            print()
        for line, text in mentions:
            print(f"L{line} MENTION: {text}")
            print()


def _is_error_result(block):
    """Check if a tool_result block is a definitive error (not a keyword match)."""
    if block.get("is_error"):
        return True
    text = get_tool_result_text(block)
    return "<tool_use_error>" in text or "<error>" in text


# Patterns that indicate a real error at the start of a tool result,
# not just the word "error" appearing somewhere in file contents.
_RESULT_ERROR_PATTERNS = re.compile(
    r"^(Error:|error:|Exit code [1-9]|FAIL|fatal:|panic:|Traceback \(most recent)",
    re.MULTILINE,
)


def cmd_audit(args):
    """Combined summary + errors + tool breakdown."""
    _check_file_type(args.file)

    messages = list(iter_messages(args.file))
    s = _accumulate_stats(args.file, messages=messages)
    _print_stats(s)

    errors, mentions = _collect_errors(args.file, args.max_width, messages=messages)
    if errors or mentions:
        print()
        print(f"Errors ({len(errors)}):")
        for line, text in errors:
            print(f"  L{line}: {text}")
        if mentions:
            print()
            print(f"Mentions ({len(mentions)}):")
            for line, text in mentions:
                print(f"  L{line}: {text}")
    else:
        print()
        print("Errors: none")


def cmd_search(args):
    _check_file_type(args.file)
    pattern = re.compile(args.pattern, re.IGNORECASE)
    max_w = args.max_width
    found = False

    for i, role, msg, _raw in iter_messages(args.file, args.line_range):
        for btype, block in extract_content_blocks(msg):
            text = ""
            if btype == "text":
                text = block if isinstance(block, str) else block.get("text", "")
            elif btype == "tool_result":
                text = get_tool_result_text(block)
            elif btype == "tool_use":
                text = json.dumps(block.get("input", {}))

            if text and pattern.search(text):
                found = True
                label = f"{role}"
                if btype == "tool_use":
                    label += f" tool:{block.get('name', '?')}"
                elif btype == "tool_result":
                    label += " result"
                print(f"L{i} [{label}]: {truncate(text.strip(), max_w)}")
                print()

    if not found:
        print(f"No matches for /{args.pattern}/")


# --- Sandbox log parsing (OCSF network events) ---

_OCSF_LINE = re.compile(
    r"\[(?P<ts>[\d.]+)\] \[sandbox\] \[(?P<level>\w+)\s*\] \[ocsf\] "
    r"(?P<event>\S+) \[(?P<severity>\w+)\] (?P<rest>.*)"
)

_NET_DEST = re.compile(r"->\s+(?P<host>[^:]+):(?P<port>\d+)")
_NET_VERDICT = re.compile(r"(?P<verdict>ALLOWED|DENIED)")
_NET_PROCESS = re.compile(r"(?P<proc>/\S+)\((?P<pid>\d+)\)")
_HTTP_METHOD_URL = re.compile(r"(?P<method>GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+(?P<url>\S+)")
_NET_POLICY = re.compile(r"\[policy:(?P<policy>\S+)\s+engine:(?P<engine>\S+)\]")


def parse_sandbox_log(path):
    """Parse OCSF lines from a sandbox log file. Yields dicts."""
    with open(path) as f:
        for line in f:
            m = _OCSF_LINE.search(line)
            if not m:
                continue
            entry = {
                "ts": float(m.group("ts")),
                "level": m.group("level"),
                "event": m.group("event"),
                "severity": m.group("severity"),
                "raw": m.group("rest"),
            }
            rest = m.group("rest")

            vm = _NET_VERDICT.search(rest)
            if vm:
                entry["verdict"] = vm.group("verdict")

            dm = _NET_DEST.search(rest)
            if dm:
                entry["host"] = dm.group("host")
                entry["port"] = int(dm.group("port"))

            pm = _NET_PROCESS.search(rest)
            if pm:
                entry["process"] = pm.group("proc")
                entry["pid"] = int(pm.group("pid"))

            hm = _HTTP_METHOD_URL.search(rest)
            if hm:
                entry["http_method"] = hm.group("method")
                entry["http_url"] = hm.group("url")

            polm = _NET_POLICY.search(rest)
            if polm:
                entry["policy"] = polm.group("policy")

            yield entry


def _host_matches(candidate, filter_val):
    """Check if candidate host matches filter_val (exact or parent-domain)."""
    if not candidate:
        return False
    candidate = candidate.lower()
    return candidate == filter_val or candidate.endswith("." + filter_val)


def _match_network_entry(e, method_filter, host_filter):
    """Return True if an HTTP entry passes the method/host filters."""
    if not e.get("http_method"):
        return False
    if method_filter and e["http_method"].upper() not in method_filter:
        return False
    if host_filter and not _host_matches(e.get("host"), host_filter):
        url_host = urlsplit(e.get("http_url", "")).hostname or ""
        if not _host_matches(url_host, host_filter):
            return False
    return True


def cmd_network(args):
    entries = list(parse_sandbox_log(args.file))

    if not entries:
        print("No OCSF events found.")
        return

    method_filter = (
        {m.strip().upper() for m in args.method.split(",")}
        if hasattr(args, "method") and args.method
        else None
    )
    host_filter = args.host.lower() if hasattr(args, "host") and args.host else None

    if args.json_output:
        if method_filter or host_filter:
            entries = [e for e in entries if _match_network_entry(e, method_filter, host_filter)]
        print(json.dumps(entries, indent=2))
        return

    t0 = entries[0]["ts"]
    t1 = entries[-1]["ts"]
    duration = t1 - t0

    event_counts = Counter(e["event"] for e in entries)
    host_counts = Counter()
    denied = []
    http_requests = []
    policies = Counter()

    for e in entries:
        h = e.get("host")
        if h:
            host_counts[h] += 1
        if e.get("verdict") == "DENIED":
            denied.append(e)
        if _match_network_entry(e, method_filter, host_filter):
            http_requests.append(e)
        p = e.get("policy")
        if p and p != "-":
            policies[p] += 1

    mins, secs = divmod(duration, 60)
    print(f"Duration:   {int(mins)}m {secs:.1f}s")
    print(f"Events:     {len(entries)}")
    print()

    if denied:
        print(f"DENIED ({len(denied)}):")
        for e in denied:
            proc = e.get("process", "?")
            host = e.get("host", "?")
            port = e.get("port", "?")
            print(f"  {proc} -> {host}:{port}")
            reason = e.get("raw", "")
            rm = re.search(r"\[reason:([^\]]+)\]", reason)
            if rm:
                print(f"    reason: {rm.group(1)}")
        print()

    print("Hosts:")
    for host, count in host_counts.most_common():
        print(f"  {host:50s} {count:>4d}")
    print()

    if policies:
        print("Policies:")
        for policy, count in policies.most_common():
            print(f"  {policy:50s} {count:>4d}")
        print()

    print("Event types:")
    for event, count in event_counts.most_common():
        print(f"  {event:20s} {count:>4d}")

    if args.http:
        filter_desc = ""
        if method_filter:
            filter_desc += f" [{','.join(sorted(method_filter))}]"
        if host_filter:
            filter_desc += f" [host={host_filter}]"
        print()
        print(f"HTTP requests{filter_desc}:")
        if not http_requests:
            print("  (none matching filters)")
        for e in http_requests:
            ts_rel = e["ts"] - t0
            method = e.get("http_method", "?")
            host = e.get("host", "?")
            url = e.get("http_url", "?")
            print(f"  +{ts_rel:7.1f}s  {method:6s} {host}  {url}")


def cmd_network_search(args):
    pattern = re.compile(args.pattern, re.IGNORECASE)
    entries = list(parse_sandbox_log(args.file))

    if not entries:
        print("No OCSF events found.")
        return

    found = False
    for e in entries:
        raw = e.get("raw", "")
        host = e.get("host", "")
        url = e.get("http_url", "")
        if pattern.search(raw) or pattern.search(host) or pattern.search(url):
            found = True
            ts = e["ts"]
            event = e["event"]
            severity = e["severity"]
            print(f"[{ts}] {event} [{severity}] {raw}")

    if not found:
        print(f"No matches for /{args.pattern}/")


def main():
    parser = argparse.ArgumentParser(
        description="Analyze fullsend agent JSONL transcripts",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    sub = parser.add_subparsers(dest="command", required=True)

    lines_help = "restrict to line range (N-M, N-, or N)"
    width_help = "truncate text to N chars (0=unlimited)"
    json_help = "output as JSON"

    p_summary = sub.add_parser("summary", help="high-level run overview")
    p_summary.add_argument("file", help="path to .jsonl file (or - for stdin)")
    p_summary.add_argument("--json", dest="json_output", action="store_true", help=json_help)
    p_summary.add_argument("--lines", dest="line_range_spec", help=lines_help)

    p_conv = sub.add_parser(
        "conversation", aliases=["conv"], help="human-readable conversation flow"
    )
    p_conv.add_argument("file", help="path to .jsonl file (or - for stdin)")
    p_conv.add_argument("--max-width", type=int, default=400, help=width_help)
    p_conv.add_argument("--lines", dest="line_range_spec", help=lines_help)

    p_tools = sub.add_parser("tools", help="tool usage table")
    p_tools.add_argument("file", help="path to .jsonl file (or - for stdin)")
    p_tools.add_argument("--json", dest="json_output", action="store_true", help=json_help)
    p_tools.add_argument("--lines", dest="line_range_spec", help=lines_help)

    p_errors = sub.add_parser("errors", help="extract errors and failures")
    p_errors.add_argument("file", help="path to .jsonl file (or - for stdin)")
    p_errors.add_argument("--max-width", type=int, default=400, help=width_help)
    p_errors.add_argument("--lines", dest="line_range_spec", help=lines_help)

    p_audit = sub.add_parser("audit", help="summary + errors + tools in one pass")
    p_audit.add_argument("file", help="path to .jsonl file (or - for stdin)")
    p_audit.add_argument("--max-width", type=int, default=400, help=width_help)

    p_search = sub.add_parser("search", help="search tool results and text for pattern")
    p_search.add_argument("pattern", help="regex pattern to search for")
    p_search.add_argument("file", help="path to .jsonl file (or - for stdin)")
    p_search.add_argument("--max-width", type=int, default=400, help=width_help)
    p_search.add_argument("--lines", dest="line_range_spec", help=lines_help)

    p_network = sub.add_parser("network", aliases=["net"], help="sandbox network activity summary")
    p_network.add_argument("file", help="path to sandbox .log file")
    p_network.add_argument("--json", dest="json_output", action="store_true", help=json_help)
    p_network.add_argument("--http", action="store_true", help="list individual HTTP requests")
    p_network.add_argument(
        "--method", help="filter HTTP requests by method (e.g. POST or POST,PUT,PATCH)"
    )
    p_network.add_argument(
        "--host", help="filter HTTP requests by host (exact or parent domain match)"
    )

    p_netsearch = sub.add_parser(
        "network-search", aliases=["netsearch"], help="search sandbox network logs"
    )
    p_netsearch.add_argument("pattern", help="regex pattern to search for")
    p_netsearch.add_argument("file", help="path to sandbox .log file")

    args = parser.parse_args()

    if hasattr(args, "line_range_spec") and args.line_range_spec:
        args.line_range = parse_line_range(args.line_range_spec)
    else:
        args.line_range = None

    if not hasattr(args, "json_output"):
        args.json_output = False

    if not hasattr(args, "max_width"):
        args.max_width = 400
    elif args.max_width == 0:
        args.max_width = None

    commands = {
        "summary": cmd_summary,
        "conversation": cmd_conversation,
        "conv": cmd_conversation,
        "tools": cmd_tools,
        "errors": cmd_errors,
        "audit": cmd_audit,
        "search": cmd_search,
        "network": cmd_network,
        "net": cmd_network,
        "network-search": cmd_network_search,
        "netsearch": cmd_network_search,
    }
    commands[args.command](args)


if __name__ == "__main__":
    main()
