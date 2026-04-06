# Triage Summary

**Title:** Desktop app crashes on manual Save with large CSV-imported task list — possible encoding error

## Problem
TaskFlow 2.3.1 desktop app crashes (closes entirely) when the user clicks the Save button on a task list containing approximately 200 tasks that were imported from a CSV file. A dialog mentioning 'encoding' flashes briefly before the app terminates. Auto-save does not trigger the crash — only manual Save does.

## Root Cause Hypothesis
The CSV import likely introduced characters with non-standard or mixed encoding (e.g., UTF-8 BOM, Latin-1 characters, or null bytes). The manual Save code path appears to handle serialization or encoding differently from auto-save — it likely performs a full re-encode or validation step that chokes on these characters, triggering an unhandled exception that terminates the app.

## Reproduction Steps
  1. Import a set of tasks from a CSV file (approximately 200 tasks) into TaskFlow
  2. Open the imported task list for editing
  3. Click the Save button in the toolbar
  4. Observe the app crash — a dialog briefly mentioning 'encoding' may flash before the app closes

## Environment
macOS 14.2 (Sonoma), TaskFlow 2.3.1 desktop app

## Severity: high

## Impact
Users who import tasks from CSV files lose unsaved work when attempting to manually save. This affects any user relying on CSV import for bulk task entry. The crash is deterministic and blocks a core workflow.

## Recommended Fix
Investigate the difference between the manual Save and auto-save code paths, focusing on character encoding and serialization. Check how CSV-imported data is stored internally versus how the manual Save serializes it. Add proper encoding handling (likely UTF-8 normalization) to the manual Save path, and wrap the save operation in error handling that surfaces the encoding error to the user rather than crashing.

## Proposed Test Case
Create a CSV file containing tasks with mixed encoding characters (e.g., accented characters, special symbols, BOM markers, non-ASCII punctuation). Import the CSV into TaskFlow, then perform a manual Save. Verify the save completes without crashing and that the saved data preserves the original characters correctly.

## Information Gaps
- Exact text of the encoding error dialog
- Contents or encoding of the original CSV file that was imported
- Whether the crash occurs with smaller CSV imports or only at scale
- Application crash logs from macOS Console or TaskFlow log directory
