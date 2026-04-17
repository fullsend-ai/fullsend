# Triage Summary

**Title:** Desktop app crashes on save with large task list imported from CSV

## Problem
The desktop app (macOS) crashes immediately when the user clicks Save in the toolbar while editing a task list containing approximately 200 tasks. The crash began occurring after the user imported tasks from a CSV file. An error dialog appears briefly before the app closes but disappears too quickly to read.

## Root Cause Hypothesis
The CSV import likely introduced malformed data, excessively long fields, or special characters that the save/serialization path cannot handle. With ~200 tasks, this could also be a memory or payload-size issue during serialization, but the fact that it worked before the CSV import and broke after strongly points to bad data introduced by the import rather than a pure scale problem.

## Reproduction Steps
  1. Create or use a task list in the desktop app
  2. Import a large number of tasks (~200) from a CSV file
  3. Edit the task list
  4. Click Save in the toolbar
  5. Observe: app crashes with a briefly-visible error dialog

## Environment
macOS 14.2 (Sonoma), TaskFlow desktop app version ~2.3.1

## Severity: high

## Impact
User is completely unable to save their work, resulting in repeated data loss. This blocks normal usage of the app for anyone with a CSV-imported task list of this size.

## Recommended Fix
Investigate the save/serialization code path for large task lists, particularly with CSV-imported data. Check for: (1) unescaped or malformed characters from CSV import that break serialization, (2) field length or row count limits in the save handler, (3) the briefly-visible error dialog — find the exception it reports (likely an uncaught error in the save routine). Adding a try/catch around the save operation to surface the actual error would be a good first step. Also review the CSV import path for insufficient validation.

## Proposed Test Case
Import a CSV file with ~200 tasks (including edge cases: special characters, very long fields, empty fields, unicode) into a new task list, then attempt to save. Verify save completes without crashing and data round-trips correctly.

## Information Gaps
- Exact crash log / stack trace (reporter declined to retrieve macOS crash reports)
- Contents or structure of the CSV file used for import
- Whether the crash occurs with fewer CSV-imported tasks (threshold)
