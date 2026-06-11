---
description: Show top RICE-scored issues plus your assigned work
argument-hint: "[--top N] [--project N] [--repo owner/name] [--user LOGIN]"
allowed-tools: Bash(python3 skills/topissues/scripts/topissues.py:*)
---

Follow skill **topissues**.

From the repository root, run:

    python3 skills/topissues/scripts/topissues.py $ARGUMENTS

Print the script stdout verbatim as the user-facing answer (markdown table). If the script fails, show stderr and do not invent scores.
