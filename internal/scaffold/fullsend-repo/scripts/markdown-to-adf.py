#!/usr/bin/env python3
"""Convert markdown-style text to Jira Atlassian Document Format (ADF).

Usage:
    echo "## Heading\n\nSome **bold** text" | python3 markdown-to-adf.py
    python3 markdown-to-adf.py < comment.md

Outputs a JSON object suitable for the Jira REST API comment body field.
Handles: headings, bold, code, links, bullet lists, horizontal rules, paragraphs.
"""

import json
import re
import sys
from urllib.parse import urlparse

MAX_INPUT_BYTES = 128 * 1024
ALLOWED_SCHEMES = {"http", "https", "mailto", ""}


def parse_inline(text: str) -> list:
    """Parse inline markdown (bold, code, links) into ADF inline nodes.

    The input must be a single line — newlines are not permitted in ADF text
    nodes.  Use parse_inline_multiline() for text that may span lines.
    """
    nodes = []
    pos = 0
    pattern = re.compile(
        r"(?P<bold>\*\*(.+?)\*\*)"
        r"|(?P<code>`([^`]+)`)"
        r"|(?P<link>\[([^\]]+)\]\(([^)]+)\))"
    )
    for m in pattern.finditer(text):
        if m.start() > pos:
            plain = text[pos : m.start()]
            if plain:
                nodes.append({"type": "text", "text": plain})
        if m.group("bold"):
            nodes.append(
                {
                    "type": "text",
                    "text": m.group(2),
                    "marks": [{"type": "strong"}],
                }
            )
        elif m.group("code"):
            nodes.append(
                {
                    "type": "text",
                    "text": m.group(4),
                    "marks": [{"type": "code"}],
                }
            )
        elif m.group("link"):
            href = m.group(7)
            scheme = urlparse(href).scheme.lower()
            if scheme in ALLOWED_SCHEMES:
                nodes.append(
                    {
                        "type": "text",
                        "text": m.group(6),
                        "marks": [{"type": "link", "attrs": {"href": href}}],
                    }
                )
            else:
                nodes.append({"type": "text", "text": f"{m.group(6)} ({href})"})
        pos = m.end()
    if pos < len(text):
        remainder = text[pos:]
        if remainder:
            nodes.append({"type": "text", "text": remainder})
    if not nodes and text:
        nodes.append({"type": "text", "text": text})
    return nodes


def parse_inline_multiline(text: str) -> list:
    """Parse inline markdown, inserting hardBreak nodes for newlines.

    Jira's ADF validator rejects literal newline characters inside text nodes.
    This function splits on newlines and inserts {"type": "hardBreak"} between
    line segments so the output is valid ADF.
    """
    lines = text.split("\n")
    nodes: list = []
    for i, line in enumerate(lines):
        if line:
            nodes.extend(parse_inline(line))
        if i < len(lines) - 1:
            nodes.append({"type": "hardBreak"})
    # Strip trailing hardBreaks (from trailing empty lines)
    while nodes and nodes[-1].get("type") == "hardBreak":
        nodes.pop()
    return nodes if nodes else [{"type": "text", "text": " "}]


def text_to_adf(text: str) -> dict:
    """Convert markdown-style text to an ADF document."""
    doc = {"type": "doc", "version": 1, "content": []}
    blocks = re.split(r"\n{2,}", text.strip())

    for block in blocks:
        block = block.strip()
        if not block:
            continue

        if block == "---":
            doc["content"].append({"type": "rule"})
            continue

        heading_match = re.match(r"^(#{1,6})\s+(.+)$", block)
        if heading_match:
            level = len(heading_match.group(1))
            doc["content"].append(
                {
                    "type": "heading",
                    "attrs": {"level": level},
                    "content": parse_inline(heading_match.group(2)),
                }
            )
            continue

        lines = block.split("\n")

        if all(re.match(r"^\s*[-*]\s+", line) for line in lines if line.strip()):
            list_node = {"type": "bulletList", "content": []}
            for line in lines:
                item_text = re.sub(r"^\s*[-*]\s+", "", line).strip()
                if item_text:
                    list_node["content"].append(
                        {
                            "type": "listItem",
                            "content": [{"type": "paragraph", "content": parse_inline(item_text)}],
                        }
                    )
            if list_node["content"]:
                doc["content"].append(list_node)
            continue

        if all(re.match(r"^\s*\d+[.)]\s+", line) for line in lines if line.strip()):
            list_node = {"type": "orderedList", "content": []}
            for line in lines:
                item_text = re.sub(r"^\s*\d+[.)]\s+", "", line).strip()
                if item_text:
                    list_node["content"].append(
                        {
                            "type": "listItem",
                            "content": [{"type": "paragraph", "content": parse_inline(item_text)}],
                        }
                    )
            if list_node["content"]:
                doc["content"].append(list_node)
            continue

        table_match = re.match(r"^\|", block)
        if table_match:
            table_lines = [
                row for row in lines if row.strip() and not re.match(r"^\|[-\s|]+\|$", row)
            ]
            if len(table_lines) >= 1:
                table_node = {"type": "table", "attrs": {"layout": "default"}, "content": []}
                for i, tl in enumerate(table_lines):
                    cells = [c.strip() for c in tl.strip("|").split("|")]
                    cell_type = "tableHeader" if i == 0 else "tableCell"
                    row = {"type": "tableRow", "content": []}
                    for cell in cells:
                        row["content"].append(
                            {
                                "type": cell_type,
                                "content": [{"type": "paragraph", "content": parse_inline(cell)}],
                            }
                        )
                    table_node["content"].append(row)
                doc["content"].append(table_node)
                continue

        mixed_content = []
        in_list = False
        list_type = "bulletList"
        list_items = []

        def _flush_list():
            nonlocal list_items, in_list, list_type
            if list_items:
                ln = {"type": list_type, "content": []}
                for it in list_items:
                    ln["content"].append(
                        {
                            "type": "listItem",
                            "content": [{"type": "paragraph", "content": parse_inline(it)}],
                        }
                    )
                doc["content"].append(ln)
            list_items = []
            in_list = False
            list_type = "bulletList"

        for line in lines:
            is_bullet = bool(re.match(r"^\s*[-*]\s+", line))
            is_ordered = bool(re.match(r"^\s*\d+[.)]\s+", line))
            is_list_item = is_bullet or is_ordered
            is_heading = bool(re.match(r"^#{1,6}\s+", line))

            if is_list_item:
                new_type = "orderedList" if is_ordered else "bulletList"
                if in_list and new_type != list_type:
                    _flush_list()
                if not in_list and mixed_content:
                    para_text = "\n".join(mixed_content).strip()
                    if para_text:
                        doc["content"].append(
                            {
                                "type": "paragraph",
                                "content": parse_inline_multiline(para_text),
                            }
                        )
                    mixed_content = []
                in_list = True
                list_type = new_type
                if is_ordered:
                    item_text = re.sub(r"^\s*\d+[.)]\s+", "", line).strip()
                else:
                    item_text = re.sub(r"^\s*[-*]\s+", "", line).strip()
                list_items.append(item_text)
            elif is_heading:
                _flush_list()
                if mixed_content:
                    para_text = "\n".join(mixed_content).strip()
                    if para_text:
                        doc["content"].append(
                            {
                                "type": "paragraph",
                                "content": parse_inline_multiline(para_text),
                            }
                        )
                    mixed_content = []
                hm = re.match(r"^(#{1,6})\s+(.+)$", line)
                doc["content"].append(
                    {
                        "type": "heading",
                        "attrs": {"level": len(hm.group(1))},
                        "content": parse_inline(hm.group(2)),
                    }
                )
            else:
                _flush_list()
                if line.strip() == "---":
                    if mixed_content:
                        para_text = "\n".join(mixed_content).strip()
                        if para_text:
                            doc["content"].append(
                                {
                                    "type": "paragraph",
                                    "content": parse_inline_multiline(para_text),
                                }
                            )
                        mixed_content = []
                    doc["content"].append({"type": "rule"})
                else:
                    mixed_content.append(line)

        _flush_list()
        if mixed_content:
            para_text = "\n".join(mixed_content).strip()
            if para_text:
                doc["content"].append(
                    {
                        "type": "paragraph",
                        "content": parse_inline_multiline(para_text),
                    }
                )

    if not doc["content"]:
        doc["content"].append(
            {
                "type": "paragraph",
                "content": [{"type": "text", "text": text or " "}],
            }
        )

    return doc


if __name__ == "__main__":
    raw = sys.stdin.read(MAX_INPUT_BYTES + 1)
    if len(raw) > MAX_INPUT_BYTES:
        print(f"ERROR: input exceeds {MAX_INPUT_BYTES} bytes", file=sys.stderr)
        sys.exit(1)
    adf = text_to_adf(raw)
    print(json.dumps({"body": adf}, ensure_ascii=False))
