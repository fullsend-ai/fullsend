#!/usr/bin/env python3
"""Claude Code PostToolUse hook for secret redaction.

Intercepts tool results (Bash, WebFetch, Read) and redacts secrets
before they enter the LLM context window. This prevents the agent from
seeing or leaking credentials in subsequent output.

Protocol: reads JSON from stdin (``tool_response`` preferred, ``tool_result``
fallback). Writes ``hookSpecificOutput.updatedToolOutput`` (and ``tool_result``)
when secrets are found. Exit code 0 always (never blocks).
"""

from __future__ import annotations

import json
import os
import re
import sys
from datetime import UTC, datetime

import hook_io

FINDINGS_PATH = "/sandbox/workspace/.security/findings.jsonl"

# --- Known secret prefix patterns ---

_PREFIX_PATTERNS: list[tuple[str, re.Pattern]] = [
    ("openai_key", re.compile(r"sk-(?:proj-)?[A-Za-z0-9_-]{20,}")),
    ("github_pat", re.compile(r"(?:ghp|github_pat|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{16,}")),
    ("slack_token", re.compile(r"xox[baprs]-[A-Za-z0-9\-]{10,}")),
    ("google_api_key", re.compile(r"AIza[A-Za-z0-9_-]{35}")),
    ("anthropic_key", re.compile(r"sk-ant-[A-Za-z0-9_-]{20,}")),
    ("aws_access_key", re.compile(r"AKIA[A-Z0-9]{16}")),
    (
        "aws_secret_key",
        re.compile(r"(?:aws_secret_access_key|AWS_SECRET_ACCESS_KEY)\s*[=:]\s*[A-Za-z0-9/+=]{40}"),
    ),
    ("stripe_key", re.compile(r"(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{10,}")),
    ("sendgrid_key", re.compile(r"SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}")),
    ("gitlab_pat", re.compile(r"gl(?:pat|rt|ptt|dt|ft|soat|cs)-[A-Za-z0-9_-]{20,}")),
    ("google_oauth_token", re.compile(r"ya29\.[A-Za-z0-9_-]{30,}")),
    ("aws_sts_key", re.compile(r"ASIA[A-Z0-9]{16}")),
    ("hf_token", re.compile(r"hf_[A-Za-z0-9]{20,}")),
    ("npm_token", re.compile(r"npm_[A-Za-z0-9]{36}")),
    ("pypi_token", re.compile(r"pypi-[A-Za-z0-9_-]{20,}")),
    ("digitalocean_token", re.compile(r"dop_v1_[a-f0-9]{64}")),
    ("perplexity_key", re.compile(r"pplx-[a-f0-9]{48}")),
    ("databricks_token", re.compile(r"dapi[a-f0-9]{32}")),
    ("telegram_bot", re.compile(r"\d{8,10}:[A-Za-z0-9_-]{35}")),
    (
        "auth_header",
        re.compile(
            r"(?:Authorization|authorization)\s*:\s*(?:Bearer|Basic|Token)\s+[A-Za-z0-9_.+/=-]{20,}"
        ),
    ),
]

# --- Structural patterns ---
#
# env_secret / json_secret match *shape*: a secret-bearing name assigned a
# credential-looking value. They used to fire on any 8+ character value,
# which rewrote ordinary source (``token = request.headers.authorization``
# became ``token = requ...``) — and an agent then composes edits from text
# that is not on disk. Both now require (1) a name whose components include
# a secret word (``secret``, ``token``, ``password``, ``api_key`` ...) and no
# non-secret qualifier (``_url``, ``_path``, ``_id`` ...), and (2) a value
# that looks like a credential rather than an identifier, member access,
# URL, path or placeholder (see _credential_like). Source-style assignments
# with spaces around ``=`` only count when the value is a quoted literal.

_STRONG_NAME_PARTS = frozenset(
    {
        "secret",
        "secrets",
        "token",
        "tokens",
        "password",
        "passwd",
        "pwd",
        "passphrase",
        "credential",
        "credentials",
        "apikey",
        "privatekey",
    }
)
# "key"/"auth" alone are weak (``{"key": "compound-command"}`` is ordinary
# JSON); "<qualifier> key" is a secret name.
_WEAK_NAME_PARTS = frozenset({"key", "auth"})
_KEY_QUALIFIERS = frozenset(
    {
        "api",
        "private",
        "secret",
        "access",
        "signing",
        "encryption",
        "master",
        "service",
        "account",
        "ssh",
        "client",
        "app",
        "license",
        "session",
    }
)
# A name with one of these components describes *about* a secret, not the
# secret itself: TOKEN_URL, KEY_ID, PASSWORD_POLICY, PUBLIC_KEY ...
_NOT_SECRET_PARTS = frozenset(
    {
        "url",
        "uri",
        "path",
        "file",
        "filename",
        "dir",
        "name",
        "names",
        "ids",
        "type",
        "kind",
        "mode",
        "enabled",
        "disabled",
        "count",
        "len",
        "length",
        "ttl",
        "expiry",
        "expires",
        "expiration",
        "header",
        "prefix",
        "suffix",
        "endpoint",
        "region",
        "version",
        "size",
        "format",
        "audience",
        "issuer",
        "var",
        "policy",
        "alg",
        "algorithm",
        "method",
        "scope",
        "scopes",
        "lifetime",
        "max",
        "min",
        "timeout",
        "interval",
        "required",
        "optional",
        "public",
        "page",
        "next",
        "continuation",
        "cursor",
        "pagination",
        "paging",
        "placeholder",
        "example",
    }
)

# Split snake_case, kebab-case, camelCase and ALLCAPS names into components.
_NAME_SPLIT_RE = re.compile(r"[^A-Za-z0-9]+|(?<=[a-z0-9])(?=[A-Z])|(?<=[A-Z])(?=[A-Z][a-z])")


def name_strength(name: str) -> str | None:
    """Return "strong", "weak" or None for a variable/JSON key name."""
    parts = [p.lower() for p in _NAME_SPLIT_RE.split(name) if p]
    if not parts or any(p in _NOT_SECRET_PARTS for p in parts):
        return None
    if parts[-1] == "id":
        # KEY_ID / CLIENT_ID name an identifier; id_token / ID_TOKEN is a token.
        return None
    if any(p in _STRONG_NAME_PARTS for p in parts):
        return "strong"
    if "key" in parts and any(p in _KEY_QUALIFIERS for p in parts):
        return "strong"
    if any(p in _WEAK_NAME_PARTS for p in parts):
        return "weak"
    return None


_VALUE_CLASS = r"[A-Za-z0-9_.+/=@:%-]"
_PLACEHOLDER_RE = re.compile(
    r"^(?:changeme|change[-_]me|password|passw0rd|passwd|secret|example|placeholder|dummy"
    r"|sample|test|testing|redacted|replace[-_]?me|your[-_][A-Za-z_-]*|[xX]{3,}|\.{3,}|_+|-+|\*+)$",
    re.IGNORECASE,
)
_IDENT_PATH_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+$")
_URL_PATH_FLAG_RE = re.compile(r"^(?:[A-Za-z][A-Za-z0-9+.-]*://|\.{0,2}/|~/|-)")
_FILE_EXT_RE = re.compile(r"\.[A-Za-z]{1,5}$")
_MASKED_RE = re.compile(r"^(?:.{4}\.\.\.|\*\*\*)$")
# A value that is itself a constant/env-var name: SecretWIFProvider = "FULLSEND_GCP_WIF_PROVIDER".
_CONSTANT_NAME_RE = re.compile(r"^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+$")
# Known token prefixes followed by words, not by token material: a fixture
# (``ghs_maskable``, ``glpat-new``, ``ghp_test123``). Real tokens with these
# prefixes are matched by the prefix patterns above, which run first.
_KNOWN_PREFIX_RE = re.compile(
    r"^(?:gh[opsur]_|github_pat_|glpat-|glrt-|sk-|xox[abpr]-|AKIA|ASIA|ya29\.|AIza|hf_|npm_|pypi-|dop_v1_|pplx-|dapi)"
    r"(?P<rest>.*)$"
)


def _fake_prefixed(value: str) -> bool:
    match = _KNOWN_PREFIX_RE.match(value)
    if not match:
        return False
    rest = match.group("rest")
    return bool(rest) and (rest.isalpha() or _word_phrase(rest) or _fixture_marker(rest))


_SEGMENT_SPLIT_RE = re.compile(r"[-_.]+")
_SECRET_WORDS = frozenset({"token", "secret", "password", "passwd", "key", "credential", "auth"})
_FIXTURE_WORDS = frozenset(
    {
        "test",
        "fake",
        "dummy",
        "example",
        "sample",
        "placeholder",
        "mock",
        "stub",
        "e2e",
        "redacted",
        "changeme",
        "your",
    }
)


def _random_segment(segment: str) -> bool:
    """A dotted segment that is base64/hex-like rather than a word (JWT parts)."""
    return (
        len(segment) >= 8
        and any(c.isalpha() for c in segment)
        and any(c.isdigit() for c in segment)
    )


def _word_phrase(value: str) -> bool:
    """True for kebab/snake phrases of plain words: ``test-secret``,
    ``ghs_policy_token``, ``page-2-token``, ``ya29.test-access-token``.
    Fixtures and identifiers are shaped like this; credentials are not."""
    parts = [s for s in _SEGMENT_SPLIT_RE.split(value) if s]
    if len(parts) < 2:
        return False
    return all(_word_segment(s) for s in parts)


def _word_segment(segment: str) -> bool:
    """A word (``policy``, ``cachedToken``), a number of up to three digits,
    a short tag (``e2e``, ``v2``, ``ya29``) or a word with a short numeric
    suffix (``test123``, ``user1``)."""
    if segment.isalpha() or (segment.isdigit() and len(segment) <= 3):
        return True
    if len(segment) <= 4 and segment.isalnum() and not segment.isdigit():
        return True
    stripped = segment.rstrip("0123456789")
    return stripped.isalpha() and len(segment) - len(stripped) <= 3


def _names_a_secret(value: str) -> bool:
    """``cached-token``, ``ghs_policy_token``, ``test-secret``: a phrase whose
    words include the kind of secret it stands in for is a fixture; a real
    credential does not call itself one."""
    return any(part.lower() in _SECRET_WORDS for part in _SEGMENT_SPLIT_RE.split(value))


def _fixture_marker(value: str) -> bool:
    """A phrase that names itself as fake: ``test-token-abc``, ``glpat-xxxx``."""
    for part in _SEGMENT_SPLIT_RE.split(value):
        word = part.lower().rstrip("0123456789")  # test123 → test
        if word in _FIXTURE_WORDS or (len(part) >= 3 and len(set(part.lower())) == 1):
            return True
    return False


def _credential_like(value: str, strength: str, *, literal: bool = True) -> bool:
    """True when ``value`` is shaped like a credential, not like code.

    Identifiers and member paths (``os.environ``), URLs, file paths, flags
    and placeholders are never credentials. A strong name needs 8+ characters
    drawn from two character classes (or 16+ of one); a weak name needs 20+
    characters including a digit or an uppercase letter. Word phrases
    (``test-secret``) are fixtures when they appear as quoted literals in
    source or JSON; in an env-style ``NAME=value`` line they are only skipped
    when they name themselves as fake (``test``, ``fake``, ``xxxx``).
    """
    if _PLACEHOLDER_RE.match(value) or _URL_PATH_FLAG_RE.match(value):
        return False
    if (literal and _CONSTANT_NAME_RE.match(value)) or _fake_prefixed(value):
        return False
    if _IDENT_PATH_RE.match(value) and not any(_random_segment(s) for s in value.split(".")):
        return False
    if _FILE_EXT_RE.search(value):
        stem = value.rsplit(".", 1)[0]
        if stem.isalpha() or _word_phrase(stem) or _IDENT_PATH_RE.match(stem):
            return False
    if _word_phrase(value) and (
        strength == "weak" or _fixture_marker(value) or (literal and _names_a_secret(value))
    ):
        return False
    has_lower = any(c.islower() for c in value)
    has_upper = any(c.isupper() for c in value)
    has_digit = any(c.isdigit() for c in value)
    has_punct = any(not c.isalnum() and c != "_" for c in value)
    classes = sum((has_lower, has_upper, has_digit, has_punct))
    if strength == "strong":
        # ``PASSWORD=letmeinn`` in a .env file is a real (weak) credential; the
        # same 8 lowercase letters as a quoted literal in source are usually a
        # fixture, so literals need a second character class or 16+ chars.
        return len(value) >= 8 and (classes >= 2 or len(value) >= 12 or not literal)
    return len(value) >= 20 and classes >= 2 and (has_digit or has_upper)


# NAME=value / export NAME=value / name = "value". Group 4 is the opening
# quote (empty when bare); the closing quote must match it. The chain driver
# additionally re-runs redact_text on an NFKC copy of each field (fullwidth
# obfuscation); the standalone main() below does not — adapters call the chain.
_ENV_ASSIGN_RE = re.compile(
    r"(?:^|(?<=[\s;,(]))(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)([ \t]*)=([ \t]*)(['\"]?)"
    r"(" + _VALUE_CLASS + r"{8,})\4",
    re.MULTILINE,
)
# What may follow an unquoted value for it to be a value and not the head of an
# expression: ``token=fetchToken()`` / ``key=cfg[0]`` / ``secret=obj.attr``.
_EXPRESSION_TAIL = frozenset("([{")
_IDENTIFIER_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
# "name": "value" / 'name': 'value' / name: "value" (JSON, dicts, YAML).
_KV_RE = re.compile(
    r"""(?:(["'])([^"'\n]{1,64})\1|(?<![A-Za-z0-9_.-])([A-Za-z_][A-Za-z0-9_-]{0,63}))"""
    r"""\s*:\s*(["'])(""" + _VALUE_CLASS + r"""{8,})\4"""
)

_PRIVATE_KEY_RE = re.compile(
    r"-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----"
    r"[\s\S]*?"
    r"-----END (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----",
)
_DB_PASSWORD_RE = re.compile(
    r"(?:postgres|mysql|mongodb|redis)(?:ql)?://[^:/\s]+:([^\s]{4,})@[^@\s/]+",
    re.IGNORECASE,
)


def structural_secrets(text: str) -> list[tuple[str, str]]:
    """Return (pattern, value) pairs for env/JSON-shaped secrets in ``text``."""
    hits: list[tuple[str, str]] = []
    for match in _ENV_ASSIGN_RE.finditer(text):
        name, space_before, space_after, quote, value = match.groups()
        strength = name_strength(name)
        if strength is None:
            continue
        if (space_before or space_after) and not quote:
            # ``token = request.headers.authorization`` — an expression, not a
            # literal. Only a quoted literal counts in source-style assignments.
            continue
        tail = text[match.end() : match.end() + 1]
        if not quote and tail in _EXPRESSION_TAIL:
            # ``Client(token=fetchToken(), api_key=loadApiKey(cfg))`` — the
            # "value" is the start of a call or subscript.
            continue
        head = text[: match.start()].rstrip(" \t")[-1:]
        if not quote and head and head in "(," and _IDENTIFIER_RE.match(value):
            # ``Client(token=accessToken, password=userPassword)`` — a keyword
            # argument whose value is a variable, not a literal.
            continue
        literal = bool(space_before or space_after)
        if _credential_like(value, strength, literal=literal):
            hits.append(("env_secret", value))
    for match in _KV_RE.finditer(text):
        name = match.group(2) or match.group(3) or ""
        value = match.group(5)
        strength = name_strength(name)
        if strength is not None and _credential_like(value, strength):
            hits.append(("json_secret", value))
    for match in _DB_PASSWORD_RE.finditer(text):
        value = match.group(1)
        if _PLACEHOLDER_RE.match(value) or value[:1] in "$<{" or _MASKED_RE.match(value):
            continue
        hits.append(("db_password", value))
    return hits


def log_finding(name: str, detail: str):
    trace_id = os.environ.get("FULLSEND_TRACE_ID", "")
    finding = {
        "trace_id": trace_id,
        "timestamp": datetime.now(UTC).isoformat(),
        "phase": "hook_posttool",
        "scanner": "secret_redact_posttool",
        "name": name,
        "severity": "high",
        "detail": detail,
        "action": "redact",
    }
    try:
        with open(FINDINGS_PATH, "a") as f:
            f.write(json.dumps(finding) + "\n")
    except OSError:
        pass


def mask_token(token: str) -> str:
    if len(token) < 10:
        return "***"
    return f"{token[:4]}..."


def redact_text(text: str) -> tuple[str, list[dict]]:
    findings: list[dict] = []
    result = text

    for name, pattern in _PREFIX_PATTERNS:
        for match in pattern.finditer(result):
            token = match.group(0)
            masked = mask_token(token)
            if masked != token:
                findings.append({"pattern": name, "masked": masked})
                result = result.replace(token, masked)

    for match in _PRIVATE_KEY_RE.finditer(result):
        block = match.group(0)
        findings.append({"pattern": "private_key", "masked": "[REDACTED PRIVATE KEY]"})
        result = result.replace(block, "[REDACTED PRIVATE KEY]")

    for name, token in structural_secrets(result):
        masked = mask_token(token)
        if masked != token and token in result:
            findings.append({"pattern": name, "masked": masked})
            result = result.replace(token, masked)

    return result, findings


MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB


def main():
    try:
        raw = sys.stdin.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            # Oversized input — truncate and scan what fits rather than
            # skipping entirely (post-tool, never blocks).
            raw = raw[:MAX_INPUT_BYTES]
        if not raw.strip():
            sys.exit(0)
        hook_input = json.loads(raw)
    except (json.JSONDecodeError, Exception):
        sys.exit(0)

    original = hook_io.payload(hook_input)
    findings: list[dict] = []

    def _redact_field(text: str) -> str:
        if not text:
            return text
        try:
            redacted, field_findings = redact_text(text)
        except Exception as e:
            log_finding("redaction_error", f"Redaction failed (passing original): {e}")
            return text
        findings.extend(field_findings)
        return redacted

    updated = hook_io.transform_strings(original, _redact_field)
    if not findings:
        sys.exit(0)

    for f in findings:
        log_finding(f["pattern"], f"Redacted {f['pattern']}: {f['masked']}")

    hook_io.emit_updated(
        updated,
        metadata={
            "secrets_redacted": len(findings),
            "patterns": [f["pattern"] for f in findings],
        },
    )
    sys.exit(0)


if __name__ == "__main__":
    main()
