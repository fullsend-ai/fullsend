# Triage Summary

**Title:** App crashes on save after importing large CSV task list (~200 tasks)

## Problem
TaskFlow desktop app crashes immediately (closes completely) whenever the user hits Save. A brief error dialog flashes but is unreadable. The crash began after the user imported a large number of tasks (~200) from a CSV file. It now reproduces every time.

## Root Cause Hypothesis
The CSV import likely introduced data that causes an unhandled exception during the save/serialization path — either malformed or edge-case data in one or more imported tasks (e.g., special characters, unexpected field lengths, null values) or a performance/memory issue when serializing ~200 tasks at once.

## Reproduction Steps
  1. Import a large CSV file containing approximately 200 tasks into TaskFlow
  2. Open the app normally
  3. Click Save
  4. Observe: app crashes and closes completely

## Environment
macOS 14.x, TaskFlow 2.3.1, desktop app (not web)

## Severity: high

## Impact
User is completely unable to save any work, resulting in data loss risk. The app is effectively unusable since every save attempt crashes it. Any user who imports a large CSV may hit this.

## Recommended Fix
Investigate the save/serialization code path with a large number of CSV-imported tasks. Check for: (1) unhandled exceptions during task serialization — add try/catch with logging, (2) malformed data from CSV import (special characters, encoding issues, missing required fields), (3) memory issues with large task lists. Examine macOS crash logs (~/Library/Logs/DiagnosticReports/) for the stack trace from the crash dialog the user couldn't read.

## Proposed Test Case
Import a CSV file with 200+ tasks containing varied data (including special characters, empty fields, long strings) and verify that Save completes without crashing. Add a unit test for the serialization path with edge-case task data.

## Information Gaps
- Exact macOS crash log / stack trace (developer can retrieve from DiagnosticReports if they reproduce)
- Contents or structure of the CSV file that was imported
- Whether the issue occurs with fewer imported tasks (threshold)
