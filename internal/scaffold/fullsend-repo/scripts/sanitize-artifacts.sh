#!/usr/bin/env bash
# sanitize-artifacts.sh — Redact private data from agent artifacts before upload.
#
# Keeps transcript structure intact for debugging/tracing while scrubbing:
#   - Jira issue descriptions, comment bodies, reporter emails
#   - Internal hostnames (*.atlassian.net)
#   - Email addresses
#   - Parent/linked issue descriptions
#   - Internal URLs (Slack, Google Docs, etc.)
#
# What survives:
#   - Agent reasoning and tool call commands
#   - Issue keys, summaries, statuses, labels (structural metadata)
#   - Phase markers and timing
#   - agent-result.json (the agent's synthesis, not raw input)
#   - exploration_context.json
#
# Usage: sanitize-artifacts.sh <fullsend-output-dir> <sanitized-output-dir>

set -euo pipefail

OUTPUT_DIR="${1:?Usage: sanitize-artifacts.sh <output-dir> <sanitized-dir>}"
SANITIZED_DIR="${2:?Usage: sanitize-artifacts.sh <output-dir> <sanitized-dir>}"

mkdir -p "$SANITIZED_DIR"

python3 - "$OUTPUT_DIR" "$SANITIZED_DIR" <<'PYTHON_SCRIPT'
import json
import os
import re
import sys

output_dir = sys.argv[1]
sanitized_dir = sys.argv[2]

EMAIL_RE = re.compile(r'[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}')
ATLASSIAN_HOST_RE = re.compile(r'[a-zA-Z0-9\-]+\.atlassian\.net')
INTERNAL_URL_RE = re.compile(r'https?://redhat-internal\.slack\.com/\S+')
GDOCS_URL_RE = re.compile(r'https?://docs\.google\.com/\S+')

REDACTED_EMAIL = '[redacted-email]'
REDACTED_HOST = '[redacted-host]'
REDACTED_BODY = '[redacted — private issue content]'
REDACTED_URL = '[redacted-internal-url]'

def looks_like_issue_context(obj):
    if not isinstance(obj, dict):
        return False
    return ('source' in obj and 'key' in obj
            and ('description' in obj or 'summary' in obj)
            and obj.get('source') in ('jira', 'github'))

def redact_issue_context(obj):
    obj = dict(obj)

    for field in ('description', 'body'):
        if field in obj and obj[field]:
            obj[field] = REDACTED_BODY

    if 'reporter' in obj:
        obj['reporter'] = REDACTED_EMAIL

    if 'host' in obj:
        obj['host'] = REDACTED_HOST

    if 'parent' in obj and isinstance(obj['parent'], dict):
        parent = dict(obj['parent'])
        for field in ('description', 'body'):
            if field in parent:
                parent[field] = REDACTED_BODY
        obj['parent'] = parent

    if 'comments' in obj and isinstance(obj['comments'], list):
        obj['comments'] = [
            {**c, 'body': REDACTED_BODY, 'author': REDACTED_EMAIL}
            if isinstance(c, dict) else c
            for c in obj['comments']
        ]

    if 'linked_issues' in obj and isinstance(obj['linked_issues'], list):
        redacted = []
        for li in obj['linked_issues']:
            if isinstance(li, dict):
                li = {**li}
                if 'description' in li:
                    li['description'] = REDACTED_BODY
            redacted.append(li)
        obj['linked_issues'] = redacted

    return obj

def scrub_text(text):
    text = EMAIL_RE.sub(REDACTED_EMAIL, text)
    text = ATLASSIAN_HOST_RE.sub(REDACTED_HOST, text)
    text = INTERNAL_URL_RE.sub(REDACTED_URL, text)
    text = GDOCS_URL_RE.sub(REDACTED_URL, text)
    return text

def find_json_object(text, start=0):
    """Find the next top-level JSON object in text starting from start."""
    brace_start = text.find('{', start)
    if brace_start == -1:
        return None, None, None

    depth = 0
    in_string = False
    escape_next = False

    for i in range(brace_start, len(text)):
        c = text[i]
        if escape_next:
            escape_next = False
            continue
        if c == '\\' and in_string:
            escape_next = True
            continue
        if c == '"' and not escape_next:
            in_string = not in_string
            continue
        if in_string:
            continue
        if c == '{':
            depth += 1
        elif c == '}':
            depth -= 1
            if depth == 0:
                return brace_start, i + 1, text[brace_start:i + 1]

    return None, None, None

def redact_content(content_str):
    """Redact issue-context JSON blocks from tool output (may be mixed content)."""
    if not isinstance(content_str, str):
        return content_str

    result_parts = []
    pos = 0
    found_any = False

    while pos < len(content_str):
        start, end, json_str = find_json_object(content_str, pos)

        if start is None:
            result_parts.append(scrub_text(content_str[pos:]))
            break

        result_parts.append(scrub_text(content_str[pos:start]))

        try:
            parsed = json.loads(json_str)
            if looks_like_issue_context(parsed):
                redacted = redact_issue_context(parsed)
                result_parts.append(json.dumps(redacted, indent=2))
                found_any = True
            else:
                result_parts.append(scrub_text(json_str))
        except (json.JSONDecodeError, ValueError):
            result_parts.append(scrub_text(json_str))

        pos = end

    return ''.join(result_parts)

def redact_jsonl_line(line_str):
    try:
        obj = json.loads(line_str)
    except (json.JSONDecodeError, ValueError):
        return scrub_text(line_str)

    msg = obj.get('message', {})
    contents = msg.get('content', [])

    if isinstance(contents, list):
        for item in contents:
            if not isinstance(item, dict):
                continue
            if item.get('type') == 'tool_result':
                content = item.get('content', '')
                if isinstance(content, str):
                    item['content'] = redact_content(content)
            if item.get('type') == 'text':
                text = item.get('text', '')
                if isinstance(text, str):
                    item['text'] = scrub_text(text)
            if item.get('type') == 'thinking':
                thinking = item.get('thinking', '')
                if isinstance(thinking, str):
                    item['thinking'] = scrub_text(thinking)
            if item.get('type') == 'tool_use':
                inp = item.get('input', {})
                if isinstance(inp, dict):
                    for k, v in inp.items():
                        if isinstance(v, str):
                            inp[k] = scrub_text(v)

    tool_result = obj.get('toolUseResult', {})
    if isinstance(tool_result, dict):
        for field in ('stdout', 'stderr', 'content'):
            val = tool_result.get(field, '')
            if isinstance(val, str) and val:
                tool_result[field] = redact_content(val)

    return json.dumps(obj, ensure_ascii=False)

transcript_count = 0
result_count = 0

for root, dirs, files in os.walk(output_dir):
    for fname in files:
        src = os.path.join(root, fname)
        rel = os.path.relpath(src, output_dir)
        dest = os.path.join(sanitized_dir, rel)
        os.makedirs(os.path.dirname(dest), exist_ok=True)

        if fname.endswith('.jsonl'):
            with open(src, 'r') as f_in, open(dest, 'w') as f_out:
                for line in f_in:
                    line = line.rstrip('\n')
                    if line:
                        f_out.write(redact_jsonl_line(line) + '\n')
            transcript_count += 1

        elif fname == 'agent-result.json':
            with open(src, 'r') as f_in, open(dest, 'w') as f_out:
                content = f_in.read()
                f_out.write(scrub_text(content))
            result_count += 1

        # Skip sandbox logs (openshell-gateway.log, openshell-sandbox.log)

print(f'Sanitized {transcript_count} transcript(s), {result_count} result(s)')
print(f'Skipped sandbox logs (gateway/sandbox .log files)')
PYTHON_SCRIPT

echo "Sanitize complete. Contents:"
find "$SANITIZED_DIR" -type f | sort
