# Triage Summary

**Title:** Desktop app crashes on manual save when tasks contain non-ASCII characters imported from CSV

## Problem
TaskFlow 2.3.1 desktop app crashes immediately (closes without recovery) when the user manually saves via the toolbar, but only when the task list contains tasks imported from a CSV file that include non-ASCII characters such as em-dashes and curly quotes. Auto-save does not trigger the crash. Removing the imported tasks restores normal save behavior.

## Root Cause Hypothesis
The manual save code path likely uses a different character encoding (or lacks encoding specification) compared to auto-save. When serializing task data containing non-ASCII characters (em-dashes, curly quotes from the CSV import), the manual save path probably attempts an encoding conversion that fails — the flashing 'encoding' dialog suggests an unhandled encoding exception that triggers an app-level crash handler. Auto-save may use a different serialization method or already handles UTF-8 correctly.

## Reproduction Steps
  1. Open TaskFlow 2.3.1 desktop app on macOS
  2. Prepare a CSV file containing ~200 tasks with non-ASCII characters (em-dashes, curly/smart quotes) in task names
  3. Import the CSV file into TaskFlow
  4. Click Save from the toolbar
  5. Observe: a dialog briefly flashes with the word 'encoding', then the app closes entirely

## Environment
macOS 14.2 (Sonoma), TaskFlow 2.3.1, desktop app (not web)

## Severity: high

## Impact
Users who import task data containing non-ASCII characters from CSV files cannot manually save, causing data loss. Any user working with internationalized text or content pasted from word processors is likely affected.

## Recommended Fix
Compare the encoding handling between the manual save and auto-save code paths. The manual save path likely needs to explicitly use UTF-8 encoding when serializing task data. Check for any encoding conversion or validation step in the manual save flow that is absent from auto-save. The flashing dialog suggests an error dialog is shown before the crash — adding proper error handling there would at minimum prevent the hard crash.

## Proposed Test Case
Create a task with non-ASCII characters (em-dashes '—', curly quotes '‘’“”', accented characters) via CSV import, then trigger a manual save and verify it completes without error. Also verify that round-tripping (save then reload) preserves the special characters correctly.

## Information Gaps
- Full text of the flashing error dialog — may contain a specific encoding name or stack trace
- Whether the crash also occurs if special characters are typed directly into a task (not via CSV import)
- Application crash logs from macOS Console that might reveal the exact exception
