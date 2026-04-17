# Triage Summary

**Title:** Crash on save: encoding error when saving large task lists containing special characters from CSV import

## Problem
TaskFlow crashes instantly when saving a task list containing ~200 tasks that were imported from a CSV file. A dialog briefly flashes mentioning 'encoding' before the app closes. The crash only occurs with large lists (~200 tasks); smaller subsets (~40-50 tasks) of the same imported data save without issue. Removing the imported tasks also eliminates the crash.

## Root Cause Hypothesis
The CSV import preserves non-ASCII characters (curly quotes, em-dashes, etc.) in their original encoding, but the save/serialization path fails to handle them correctly — likely a buffer overflow or unhandled encoding conversion error that becomes fatal at scale. The size dependency suggests either a fixed-size buffer for encoding conversion, or an accumulating error (e.g., character-by-character re-encoding) that only overflows or triggers a crash past a certain data volume.

## Reproduction Steps
  1. Install TaskFlow v2.3.1 on macOS 14.2
  2. Create a CSV file with ~200 task entries containing curly quotes (“ ”), em-dashes (—), and other non-ASCII Unicode characters in task names/descriptions
  3. Import the CSV into TaskFlow
  4. Click Save in the toolbar
  5. Observe: brief dialog flash mentioning 'encoding', then app closes

## Environment
macOS 14.2, TaskFlow v2.3.1

## Severity: high

## Impact
Users who import tasks from external tools (CSV) with non-ASCII characters lose all unsaved work when saving large task lists. Workaround exists (delete imported tasks or work with smaller lists), but this defeats the purpose of the import feature.

## Recommended Fix
Investigate the save/serialization path for encoding handling. Likely candidates: (1) check if the save routine assumes ASCII or a fixed encoding when writing task data — it should use UTF-8 throughout, (2) look for fixed-size buffers in the serialization layer that could overflow with multi-byte characters at scale, (3) add proper error handling around the encoding/serialization step so a conversion failure surfaces as a user-visible error rather than a crash. Also audit the CSV import path to ensure it normalizes encoding on ingest.

## Proposed Test Case
Create a test that generates a task list of 200+ entries with mixed Unicode characters (curly quotes, em-dashes, emoji, accented characters), saves it, reloads it, and verifies all characters round-trip correctly. Add a boundary test varying list size (50, 100, 200, 500 tasks) to confirm the size-dependent aspect is resolved.

## Information Gaps
- Exact error message in the flashing dialog (reporter couldn't read it fully)
- Whether auto-save also triggers the crash or only manual toolbar save
- Whether the crash occurs on other platforms (Windows, Linux) or is macOS-specific
- Exact CSV encoding (UTF-8, Windows-1252, etc.) from the exporting tool
