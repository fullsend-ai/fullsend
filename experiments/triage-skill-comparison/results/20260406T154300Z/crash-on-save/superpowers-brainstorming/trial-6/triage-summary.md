# Triage Summary

**Title:** App crashes on save with encoding error after CSV task import

## Problem
When a user edits a task list that was populated via CSV import (~200 tasks) and clicks the toolbar 'Save' button, the application crashes. A brief error dialog referencing 'encoding' flashes before the app closes. The crash is deterministic and repeatable, causing the user to lose unsaved work each time.

## Root Cause Hypothesis
The CSV import path does not normalize text encoding on ingest, allowing non-UTF-8 or mixed-encoding characters (e.g., Latin-1 smart quotes, BOM markers, or other non-ASCII byte sequences) into the task data store. The save/serialization path then attempts to encode this data (likely as UTF-8 or JSON) and throws an unhandled encoding exception, which tears down the application.

## Reproduction Steps
  1. Prepare a CSV file containing task data with non-ASCII characters (e.g., curly quotes, accented characters, or a non-UTF-8 encoding like Latin-1 or Windows-1252)
  2. Import the CSV file into TaskFlow, creating ~200 tasks
  3. Open the imported task list and edit any task
  4. Click the 'Save' button in the toolbar
  5. Observe the encoding error flash and application crash

## Environment
Not yet specified — likely desktop app (given toolbar and crash-to-close behavior). OS and version not confirmed but not needed to begin investigation.

## Severity: high

## Impact
Any user who imports tasks from a CSV file with non-standard encoding will be unable to save edits, losing work on every attempt. This blocks a core workflow (edit + save) for affected task lists.

## Recommended Fix
1. Investigate the save/serialization code path for unhandled encoding exceptions — add proper error handling so encoding failures surface a user-visible error instead of crashing. 2. Audit the CSV import path to normalize all text to UTF-8 on ingest, handling common source encodings (Latin-1, Windows-1252, UTF-16). 3. Consider a migration or repair utility for already-imported data containing bad encoding sequences.

## Proposed Test Case
Import a CSV file saved with Windows-1252 encoding containing characters like smart quotes (\u201c\u201d), em dashes (\u2014), and accented characters (é, ñ). Edit a task in the imported list and click Save. Verify the save completes without error and the characters are preserved correctly.

## Information Gaps
- Exact OS and app version (does not change the fix approach)
- The specific CSV file or its source encoding (reproducible with any non-UTF-8 CSV)
- Whether the crash also occurs when saving without editing (save of unmodified imported data)
