# 011: run-gemini-cli Output Parsing Failure (Upstream Bug)

## Problem

The `google-github-actions/run-gemini-cli` action intermittently fails when Gemini CLI output contains special characters (HTML tags like `<`, shell metacharacters). The action writes malformed data to `$GITHUB_OUTPUT`, causing GitHub Actions to reject it.

## Observed ([PR #14](https://github.com/nonflux/integration-service/pull/14))

Review agent failed on run `23774142233` with:
```
##[error]Invalid format 'SCRIPT_EOF Syntax Errors: [ 'Error node: "<" at 0:0' ]'
```

Rerunning the same job succeeded — confirming the issue is intermittent.

## Root Cause

Bug in `run-gemini-cli`'s output handling. The action uses heredoc-style delimiters for multi-line output but doesn't escape shell-significant characters (`<`, `>`).

## Status

Upstream issue filed on `google-github-actions/run-gemini-cli`. Workaround: rerun failed jobs.
