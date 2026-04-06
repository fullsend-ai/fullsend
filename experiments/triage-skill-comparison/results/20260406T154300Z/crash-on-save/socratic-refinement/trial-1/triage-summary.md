# Triage Summary

**Title:** App crashes on manual Save with encoding error after CSV task import (~200 tasks)

## Problem
After importing tasks from a CSV file, clicking the Save button in the toolbar causes the app to close immediately. A dialog briefly flashes mentioning 'encoding' before the crash. Auto-save continues to work without issue. The crash is 100% reproducible on manual save.

## Root Cause Hypothesis
The CSV import introduced task data containing characters with incompatible or malformed encoding (e.g., mixed UTF-8/Latin-1, BOM characters, or invalid byte sequences). The manual Save code path likely performs encoding validation or conversion that auto-save does not, hitting an unhandled exception on the problematic characters that terminates the process.

## Reproduction Steps
  1. Create or open a task list in TaskFlow
  2. Import a large set of tasks (~200) from a CSV file
  3. Click the Save button in the toolbar
  4. Observe: app crashes with a brief flash of an encoding-related dialog

## Environment
Not specified — reproduce with any environment using CSV import functionality

## Severity: high

## Impact
Users who import tasks from CSV files cannot manually save their work. While auto-save provides a partial workaround, users risk data loss and cannot trust the save workflow. This blocks any CSV-based import workflow.

## Recommended Fix
1. Compare the manual Save and auto-save code paths to identify where encoding handling diverges. 2. Add proper encoding detection/normalization during CSV import (sanitize on ingest). 3. Wrap the encoding conversion in the manual Save path with error handling so it surfaces a readable error instead of crashing. 4. Ensure the flashed dialog's exception is caught and logged rather than causing process termination.

## Proposed Test Case
Import a CSV file containing mixed-encoding characters (e.g., UTF-8 with Latin-1 sequences, BOM markers, and special characters like em-dashes or accented letters) into a task list, then trigger manual Save. Verify the save completes without crashing and the data is preserved correctly.

## Information Gaps
- Exact encoding of the source CSV file (UTF-8, Latin-1, Windows-1252, etc.)
- Full text of the flashed encoding error dialog
- Operating system and TaskFlow version
- Whether removing the imported tasks restores normal save behavior
