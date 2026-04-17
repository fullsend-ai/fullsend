# Triage Summary

**Title:** App crashes on manual save with encoding error after CSV task import

## Problem
When a user imports tasks from a CSV file and then clicks the 'Save' button in the toolbar, the application crashes immediately. A dialog mentioning 'encoding' flashes briefly before the app closes. Auto-save does not trigger the crash — only the manual save button does.

## Root Cause Hypothesis
The CSV import likely introduced task data containing characters that the manual save code path cannot encode (e.g., non-UTF-8 characters, BOM markers, or special/malformed Unicode). The manual save path probably uses a different serialization or encoding routine than auto-save — possibly a strict encoder that throws on unencodable characters rather than handling them gracefully, causing an unhandled exception that kills the process.

## Reproduction Steps
  1. Create or obtain a CSV file with ~200 tasks (include non-ASCII characters or mixed encodings if possible)
  2. Import the CSV file into TaskFlow
  3. Click the 'Save' button in the toolbar
  4. Observe the encoding error dialog flash and app crash

## Environment
Not specified — likely desktop app. CSV source and encoding unknown but the crash is tied to imported data, not a specific OS or version.

## Severity: high

## Impact
Users who import tasks from CSV files risk losing all unsaved work on every manual save. The workaround (relying on auto-save) is fragile and undiscoverable. Data loss is occurring.

## Recommended Fix
1. Compare the manual save and auto-save code paths to identify where they diverge on encoding/serialization. 2. Add explicit encoding handling (e.g., enforce UTF-8 with replacement or lossy conversion) in the manual save path. 3. Wrap the save operation in proper error handling so encoding failures surface as a user-facing error message rather than crashing the app. 4. Consider sanitizing or re-encoding data at CSV import time to prevent bad data from entering the system.

## Proposed Test Case
Import a CSV file containing tasks with mixed encodings (UTF-8, Latin-1, BOM markers, and emoji/special Unicode characters). Verify that clicking the manual Save button completes successfully without crashing, and that the saved data round-trips correctly when reopened.

## Information Gaps
- Exact encoding of the source CSV file and whether it contains specific problematic characters
- Operating system and app version
- Whether the crash is reproducible with a smaller number of imported tasks or requires the full ~200
