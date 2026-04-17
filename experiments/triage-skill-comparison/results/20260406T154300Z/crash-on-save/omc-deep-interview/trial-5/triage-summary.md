# Triage Summary

**Title:** Desktop app crashes on manual Save when large task list contains non-ASCII characters from CSV import

## Problem
TaskFlow v2.3.1 desktop app on macOS crashes (abrupt quit with brief 'encoding' error dialog) when the user clicks the Save button on a task list with ~200 tasks that includes special characters (em-dashes, curly quotes) imported from a CSV file. Auto-save does not trigger the crash. Smaller lists (<50 tasks) with the same special characters save without issue, and large lists without special characters also save fine.

## Root Cause Hypothesis
The manual Save code path likely uses a different (or buffer-size-limited) text encoding/serialization routine than auto-save. When the task list is large enough that the serialized payload exceeds some internal buffer or chunk boundary, non-ASCII characters (em-dashes U+2014, curly quotes U+201C/U+201D) cause an encoding conversion error — possibly a truncated multi-byte sequence at a buffer boundary — that results in an unhandled exception and app crash.

## Reproduction Steps
  1. Install TaskFlow v2.3.1 desktop app on macOS 14.2
  2. Create or import a task list with ~200 tasks (import a CSV containing em-dashes and curly quotes in task names works reliably)
  3. Click the 'Save' button in the toolbar
  4. Observe: brief dialog flash mentioning 'encoding', then app closes

## Environment
TaskFlow v2.3.1, macOS 14.2 (Sonoma), desktop app

## Severity: high

## Impact
Users who import CSV data containing non-ASCII punctuation into large task lists lose all unsaved work on crash. Workaround exists (remove special characters or rely on auto-save), but data loss risk is high for unaware users.

## Recommended Fix
Investigate the manual Save serialization path for encoding handling differences vs auto-save. Likely candidates: (1) a fixed-size buffer that splits multi-byte UTF-8 sequences, (2) an implicit ASCII encoding assumption in the Save button handler vs UTF-8 in auto-save, or (3) a missing error handler around the encoding step. Unify the Save and auto-save serialization paths or ensure the manual Save path handles UTF-8 correctly at all payload sizes. Add a crash-safe write (write to temp file, then atomic rename) to prevent data loss even on failure.

## Proposed Test Case
Create a task list with 200+ tasks where multiple task names contain multi-byte UTF-8 characters (em-dashes, curly quotes, emoji, CJK characters). Invoke the manual Save path and verify: (a) save completes without error, (b) file is valid and re-loadable, (c) all special characters are preserved. Also test boundary sizes (50, 100, 150, 200, 500 tasks) to identify the exact threshold.

## Information Gaps
- Exact error message in the flashed dialog (reporter only caught 'encoding')
- Whether the issue reproduces on Windows/Linux or is macOS-specific
- Whether emoji or CJK characters also trigger the crash or only Windows-1252 legacy characters
- Crash logs from macOS Console.app that might pinpoint the exact exception
