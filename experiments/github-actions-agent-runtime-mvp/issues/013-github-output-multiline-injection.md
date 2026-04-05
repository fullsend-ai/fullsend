# 013: GITHUB_OUTPUT Multi-Line Value Injection

## Problem

The fix agent workflow passes user-controlled values (e.g., `/fix-agent` comment body) to `$GITHUB_OUTPUT` using:

```bash
echo "instruction=$INSTRUCTION" >> "$GITHUB_OUTPUT"
```

If `$INSTRUCTION` contains newlines, an attacker can inject arbitrary output variables by crafting a multi-line comment.

## Proposed Fix

Use heredoc-style EOF delimiters for multi-line values:

```bash
cat <<'EOF' >> "$GITHUB_OUTPUT"
instruction<<SCRIPT_EOF
$INSTRUCTION
SCRIPT_EOF
EOF
```

This is the GitHub-recommended pattern for multi-line output values.
