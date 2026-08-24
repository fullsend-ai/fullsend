#!/usr/bin/env python3
"""Claude Code PreToolUse hook for SSRF protection.

Intercepts Bash and WebFetch tool calls to validate URLs against RFC 1918
private networks, cloud metadata endpoints, and dangerous schemes before
the agent can make outbound requests.

Protocol: reads JSON from stdin, writes JSON to stdout.
Exit codes: 0 = allow, 1 = block (with reason on stdout).
"""

from __future__ import annotations

import ipaddress
import json
import os
import re
import socket
import sys
from datetime import UTC, datetime

# --- Blocklists ---

BLOCKED_HOSTNAMES: set[str] = {
    "metadata.google.internal",
    "metadata.goog",
    "169.254.169.254",
    "100.100.100.200",
    "fd00:ec2::254",
}

BLOCKED_NETWORKS: list[ipaddress.IPv4Network | ipaddress.IPv6Network] = [
    ipaddress.IPv4Network("10.0.0.0/8"),
    ipaddress.IPv4Network("172.16.0.0/12"),
    ipaddress.IPv4Network("192.168.0.0/16"),
    ipaddress.IPv4Network("127.0.0.0/8"),
    ipaddress.IPv6Network("::1/128"),
    ipaddress.IPv4Network("169.254.0.0/16"),
    ipaddress.IPv6Network("fe80::/10"),
    ipaddress.IPv4Network("100.64.0.0/10"),
    ipaddress.IPv4Network("0.0.0.0/8"),
    ipaddress.IPv6Network("::/128"),
    ipaddress.IPv6Network("fc00::/7"),
]

BLOCKED_SCHEMES: set[str] = {"file", "ftp", "gopher", "data", "dict", "ldap", "tftp"}
ALLOWED_SCHEMES: set[str] = {"http", "https"}

URL_PATTERN = re.compile(
    r"""(?:https?|file|ftp|gopher|data|dict|ldap|tftp)://[^\s"'`|;<>()]+""",
    re.IGNORECASE,
)

# ``sed`` as a whole word, used to anchor the substitution heuristic below.
_SED_WORD = re.compile(r"\bsed\b")

# Pattern to find sed substitution openings: s<delim> preceded by a quote,
# semicolon, or whitespace — anchored so word-internal 's' (e.g. 'items|')
# cannot match.  Captures the delimiter character.
_SED_SUBST_OPEN = re.compile(r"(?<=[\s'\";])s([^\w\s])")

# Compact GNU sed form: ``sed -es/…`` (no space between ``-e`` and ``s``).
_SED_COMPACT_OPEN = re.compile(r"-es([^\w\s])")


# Pattern to detect quoted pattern arguments to grep/awk family commands.
# Matches: grep [-flags] 'URL   grep -E "URL   awk '/URL   etc.
# The optional trailing / covers awk regex delimiters: awk '/pattern/'.
_TEXT_CMD_QUOTED_PREFIX = re.compile(
    r"\b(?:grep|egrep|fgrep|awk|gawk|mawk)\s+(?:-\S+\s+)*['\"]/?$",
)

# Network-capable commands whose presence in a downstream pipe stage means
# a URL matched in an upstream grep/awk pattern could actually be fetched.
# Interpreters count: piping into one hands it arbitrary execution, so a
# shell is no safer here than python or perl.
_NETWORK_COMMANDS = re.compile(
    r"\b(?:curl|wget|fetch|nc|ncat|xargs"
    r"|python[23]?|ruby|perl|node"
    r"|socat|openssl|lynx|w3m|aria2c)\b"
    # Shells are matched separately: the lookbehind keeps a script's
    # extension (``install.sh``, ``deploy.bash``) from reading as an
    # interpreter, while ``sh``, ``/bin/sh`` and ``bash`` still match.
    r"|(?<![.\w])(?:bash|sh|dash|zsh|ksh)\b"
)

# Commands that only reshape or display text on stdout: they neither execute
# what they read nor persist it.  Only these may consume a grep/awk stage whose
# pattern held an exempted URL.
_PURE_VIEWERS = frozenset(
    {
        "sort",
        "head",
        "tail",
        "uniq",
        "wc",
        "cat",
        "nl",
        "tac",
        "rev",
        "column",
        "fmt",
        "fold",
        "expand",
        "unexpand",
        "tr",
        "cut",
        "less",
        "more",
    }
)

# Shell-reentry commands that spawn a new shell layer where previously-quoted
# metacharacters become active operators.  Flags may sit between the shell
# name and -c (``bash -x -c``, ``sh -l -c``, ``bash --norc -c``).
_SHELL_REENTRY = re.compile(r"\b(?:bash|sh|dash|zsh|ksh)\s+(?:-\S+\s+)*-c\b|\beval\b")

FINDINGS_PATH = "/sandbox/workspace/.security/findings.jsonl"


def log_finding(scanner: str, name: str, severity: str, detail: str, action: str):
    """Append a finding to the JSONL audit log."""
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_pretool",
        "scanner": scanner,
        "name": name,
        "severity": severity,
        "detail": detail,
        "action": action,
    }
    try:
        with open(FINDINGS_PATH, "a") as f:
            f.write(json.dumps(finding) + "\n")
    except OSError:
        pass


def check_ip(ip_str: str) -> str | None:
    try:
        ip = ipaddress.ip_address(ip_str)
    except ValueError:
        return None
    for network in BLOCKED_NETWORKS:
        if ip in network:
            return f"IP {ip} is in blocked network {network}"
    if ip.is_private:
        return f"IP {ip} is a private address"
    return None


def validate_url(url: str) -> str | None:
    try:
        from urllib.parse import urlparse

        parsed = urlparse(url)
    except Exception:
        return "Malformed URL"

    scheme = (parsed.scheme or "").lower()
    if scheme in BLOCKED_SCHEMES:
        return f"Blocked scheme: {scheme}"
    if scheme not in ALLOWED_SCHEMES:
        return f"Disallowed scheme: {scheme}"

    hostname = (parsed.hostname or "").lower().rstrip(".")
    if not hostname:
        return "No hostname in URL"
    if hostname in BLOCKED_HOSTNAMES:
        return f"Blocked hostname: {hostname}"

    ip_reason = check_ip(hostname)
    if ip_reason:
        return ip_reason

    # DNS rebinding defense: resolve hostname and check resolved IPs
    prev_timeout = socket.getdefaulttimeout()
    try:
        socket.setdefaulttimeout(2.0)
        addrinfos = socket.getaddrinfo(hostname, None, proto=socket.IPPROTO_TCP)
        for _family, _, _, _, sockaddr in addrinfos:
            resolved_ip = str(sockaddr[0])
            ip_reason = check_ip(resolved_ip)
            if ip_reason:
                return f"DNS rebinding: {hostname} resolved to blocked {resolved_ip} ({ip_reason})"
    except TimeoutError:
        return f"DNS resolution timed out for {hostname} (fail-closed)"
    except socket.gaierror:
        return f"DNS resolution failed for {hostname} (fail-closed)"
    finally:
        socket.setdefaulttimeout(prev_timeout)

    return None


def _find_unquoted_separators(command: str) -> list[tuple[int, int, str]]:
    """Return (start, end, sep) for each unquoted shell separator."""
    results: list[tuple[int, int, str]] = []
    i = 0
    in_sq = False
    in_dq = False
    n = len(command)
    while i < n:
        ch = command[i]
        if in_sq:
            if ch == "'":
                in_sq = False
            i += 1
            continue
        if in_dq:
            if ch == "\\" and i + 1 < n:
                i += 2
                continue
            if ch == '"':
                in_dq = False
            i += 1
            continue
        if ch == "'":
            in_sq = True
            i += 1
            continue
        if ch == '"':
            in_dq = True
            i += 1
            continue
        if ch == "\\" and i + 1 < n:
            i += 2
            continue
        # Two-char operators first so ``||`` is not mistaken for ``|``.
        two = command[i : i + 2]
        if two in ("&&", "||"):
            results.append((i, i + 2, two))
            i += 2
            continue
        if ch in ";|&\n":
            results.append((i, i + 1, ch))
            i += 1
            continue
        i += 1
    return results


def _segment_bounds_at(command: str, pos: int) -> tuple[int, int]:
    """Return (start, end) of the shell segment containing *pos*."""
    seps = _find_unquoted_separators(command)
    seg_start = 0
    seg_end = len(command)
    for sep_start, sep_end, _ in seps:
        if sep_end <= pos:
            seg_start = sep_end
        elif sep_start >= pos:
            seg_end = sep_start
            break
    return seg_start, seg_end


def _is_pure_stage(stage: str) -> bool:
    """Return True if *stage* only reshapes text on stdout."""
    if _has_substitution(stage) or _has_output_redirection(stage):
        return False
    tokens = stage.split()
    if not tokens:
        return False
    if tokens[0].rsplit("/", 1)[-1] not in _PURE_VIEWERS:
        return False
    # Purity is a property of the invocation, not the binary: ``sort -o FILE``
    # and ``--output=FILE`` persist without any shell redirection.
    return not any(t == "-o" or t.startswith("--output") for t in tokens[1:])


def _downstream_stages_are_pure(command: str, url_start: int) -> bool:
    """Return True if every pipe stage after *url_start* only reshapes text."""
    # grep/awk print what they match, so their output *is* the URL.  Listing the
    # consumers that could act on it is a denylist that keeps losing — curl,
    # xargs, python, bash, tee, dd, cp, split, ... Invert it: a consumer must be
    # recognised as inert, and anything unrecognised disqualifies the exemption.
    # Omitting a command from _PURE_VIEWERS therefore costs a needless block,
    # never a bypass.
    seps = _find_unquoted_separators(command)
    first_pipe = None
    for i, (sep_start, _sep_end, sep_str) in enumerate(seps):
        if sep_start < url_start:
            continue
        if sep_str != "|":
            # The separator scanner tracks quoting but not compound-command
            # nesting, so a ';' inside ``{ ...; } | consumer`` or
            # ``do ...; done | consumer`` looks like a statement end.  If any
            # pipe still follows, we cannot prove the pipeline ended here.
            return not any(t == "|" for _, _, t in seps[i + 1 :])
        first_pipe = i
        break
    if first_pipe is None:
        return True  # nothing downstream at all

    stage_start = seps[first_pipe][1]
    for sep_start, sep_end, sep_str in seps[first_pipe + 1 :]:
        if sep_str != "|":
            return _is_pure_stage(command[stage_start:sep_start])
        if not _is_pure_stage(command[stage_start:sep_start]):
            return False
        stage_start = sep_end
    return _is_pure_stage(command[stage_start:])


def _has_substitution(text: str) -> bool:
    """Return True if *text* opens a command or process substitution."""
    # ``$(...)``, backticks and process substitution ``<(...)``/``>(...)`` all
    # splice the output of a nested command into the surrounding text.  Only
    # single quotes suppress them; inside double quotes they stay active.
    i = 0
    in_sq = False
    n = len(text)
    while i < n:
        ch = text[i]
        if in_sq:
            if ch == "'":
                in_sq = False
            i += 1
            continue
        if ch == "'":
            in_sq = True
            i += 1
            continue
        if ch == "\\" and i + 1 < n:
            i += 2
            continue
        if ch == "`":
            return True
        if ch in "$<>" and i + 1 < n and text[i + 1] == "(":
            return True
        i += 1
    return False


def _has_output_redirection(segment: str) -> bool:
    """Return True if *segment* contains an unquoted ``>`` or ``>>`` redirection."""
    i = 0
    in_sq = False
    in_dq = False
    n = len(segment)
    while i < n:
        ch = segment[i]
        if in_sq:
            if ch == "'":
                in_sq = False
            i += 1
            continue
        if in_dq:
            if ch == "\\" and i + 1 < n:
                i += 2
                continue
            if ch == '"':
                in_dq = False
            i += 1
            continue
        if ch == "'":
            in_sq = True
            i += 1
            continue
        if ch == '"':
            in_dq = True
            i += 1
            continue
        if ch == "\\" and i + 1 < n:
            i += 2
            continue
        if ch == ">":
            return True
        i += 1
    return False


def _sed_script_writes_or_executes(segment: str, delim: str) -> bool:
    """Return True if the sed script can write a file or run a command."""
    # sed is not only a filter.  ``w``/``W`` write the pattern space to a file
    # and ``e`` executes it as a shell command, either as flags on a
    # substitution (``s/x/y/w out``, ``s/x/y/e``) or attached to an address
    # (``/addr/w out``).  None involve a pipe, a redirection or an external
    # binary, so nothing else here would notice them.
    #
    # Match on shape rather than counting delimiter fields: a URL containing
    # the delimiter (``s/https://x//``) makes any field count unreliable.  A
    # real write/execute is a closing delimiter, optional harmless flags, then
    # the command letter — ``w``/``W`` followed by a filename, or ``e`` at the
    # end of the script.
    # Standalone command after a previous one: ``s/x/y/; w out`` or ``;e``.
    if re.search(r"(?<!\\)[;}]\s*[wW]\s+\S", segment):
        return True
    if re.search(r"(?<!\\)[;}]\s*e(?=[\s;}'\"]|$)", segment):
        return True
    for d in {delim, "/"}:
        esc = re.escape(d)
        if re.search(rf"(?<!\\){esc}[gpiImM0-9]*[wW]\s+\S", segment):
            return True
        if re.search(rf"(?<!\\){esc}[gpiImM0-9]*e(?=[\s;}}'\"]|$)", segment):
            return True
    return False


def _is_in_text_pattern_context(command: str, match_start: int) -> bool:
    """Return True if the URL at *match_start* is inside a text-manipulation pattern."""
    # Restrict analysis to the shell segment containing the URL so that
    # ``sed`` or ``grep`` in a *different* statement cannot cause a bypass.
    seg_start, seg_end = _segment_bounds_at(command, match_start)
    segment = command[seg_start:seg_end]
    prefix = command[seg_start:match_start]

    # Shell-reentry commands (bash -c, sh -c, eval) create a second
    # shell layer where previously-quoted metacharacters become active.
    # Refuse to exempt any URL in such a segment.
    if _SHELL_REENTRY.search(segment):
        return False

    # Command and process substitution splice a nested command's output into
    # this segment, so a URL here may be fetched rather than matched as text
    # (``sed "s/$(curl URL)/x/"``, ``curl $(grep 'URL' f)``, ``sed 's@' <(curl URL)``).
    if _has_substitution(segment):
        return False

    # A network-capable command in the same segment means the segment can make
    # a request without a pipe or a substitution — awk's ``system()``/command
    # pipes and sed's GNU ``e`` flag both execute a shell from inside the
    # pattern argument (``awk '/URL/ {system("curl "$0)}' f``).
    if _NETWORK_COMMANDS.search(segment):
        return False

    # sed substitution: require 'sed' as a word in the segment prefix,
    # then verify the URL is in the search-pattern field (not the
    # replacement field).
    if _SED_WORD.search(prefix):
        # Collect openings from both the standard and compact forms.
        all_opens = list(_SED_SUBST_OPEN.finditer(prefix)) + list(
            _SED_COMPACT_OPEN.finditer(prefix)
        )
        if all_opens:
            last = max(all_opens, key=lambda m: m.end())
            delim = last.group(1)
            # Content between the s<delim> opening and the URL start.
            between = prefix[last.end() :]
            # Search field has zero delimiters before the URL; replacement
            # or flags field has one or more.
            if between.count(delim) == 0:
                # A sed script that can write a file or run a command launders
                # the URL without any pipe or redirection to notice.
                if _sed_script_writes_or_executes(segment, delim):
                    return False
                # ``s|URL|...|`` usually removes the URL, but ``&`` and ``\1``
                # reproduce the match verbatim, so sed's stdout can carry it
                # onward just like grep's.  Apply the same downstream rules.
                if not _downstream_stages_are_pure(command, match_start):
                    return False
                return not _has_output_redirection(segment)
        return False

    # Quoted argument to grep/awk family: ...grep [-flags] 'URL  or  "URL
    # Unlike sed's ``s|URL|...|``, which removes the URL from its output,
    # grep/awk *emit* what they match.  Exempt only when that output goes
    # nowhere: no further pipeline stage and no redirection to a file.
    if not _TEXT_CMD_QUOTED_PREFIX.search(prefix):
        return False
    if not _downstream_stages_are_pure(command, match_start):
        return False
    return not _has_output_redirection(segment)


def _extract_network_urls(command: str) -> list[str]:
    """Return URLs from *command* that could be outbound network targets."""
    return [
        m.group()
        for m in URL_PATTERN.finditer(command)
        if not _is_in_text_pattern_context(command, m.start())
    ]


def process_tool_call(tool_input: dict) -> str | None:
    tool_name = tool_input.get("tool_name", "")
    tool_params = tool_input.get("tool_input", {})

    urls: list[str] = []
    if tool_name == "Bash":
        command = tool_params.get("command", "")
        urls = _extract_network_urls(command)
    elif tool_name == "WebFetch":
        url = tool_params.get("url", "")
        if url:
            urls = [url]

    for url in urls:
        reason = validate_url(url)
        if reason:
            return f"SSRF blocked: {url} - {reason}"
    return None


MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB


def main():
    try:
        raw = sys.stdin.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            # Oversized input — fail closed.
            json.dump({"decision": "block", "reason": "Hook input exceeds 10 MB limit"}, sys.stdout)
            sys.exit(1)
        if not raw.strip():
            sys.exit(0)
        tool_input = json.loads(raw)
    except json.JSONDecodeError:
        # Unparseable input — fail closed (pre-tool hook must block).
        json.dump(
            {"decision": "block", "reason": "Unparseable hook input (fail-closed)"}, sys.stdout
        )
        sys.exit(1)
    except Exception as e:
        json.dump({"decision": "block", "reason": f"Hook error (fail-closed): {e}"}, sys.stdout)
        sys.exit(1)

    reason = process_tool_call(tool_input)

    if reason:
        log_finding("ssrf_pretool", "ssrf_blocked", "critical", reason, "block")
        json.dump({"decision": "block", "reason": reason}, sys.stdout)
        sys.exit(1)

    sys.exit(0)


if __name__ == "__main__":
    main()
