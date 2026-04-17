# Triage Summary

**Title:** Manual save crashes on task lists containing special Unicode characters imported via CSV

## Problem
When a user manually saves a task list (via the toolbar Save button) that contains tasks imported from a CSV file with special Unicode characters (curly quotes, em-dashes, etc. originating from a Word document), the app crashes. A brief error dialog referencing 'encoding' flashes before the app closes. Auto-save does not trigger the crash, indicating the manual save path uses a different serialization or encoding method that cannot handle these characters.

## Root Cause Hypothesis
The manual save code path likely uses a different text encoding or serialization method than auto-save. The manual save probably attempts to encode content as ASCII or a restrictive encoding (e.g., latin-1) rather than UTF-8, causing it to choke on Unicode characters like curly quotes (U+2018/U+2019), em-dashes (U+2014), and similar characters introduced by the CSV import from a Word document. Auto-save likely uses a different writer or encoding setting that handles UTF-8 correctly.

## Reproduction Steps
  1. Create a CSV file containing tasks with special Unicode characters (curly quotes, em-dashes — characters typically produced by Microsoft Word)
  2. Import the CSV into TaskFlow as a new task list or into an existing one
  3. Click the 'Save' button in the toolbar
  4. Observe: an error dialog briefly flashes mentioning 'encoding', then the app crashes

## Environment
Not platform-specific based on available information. Triggered by content (Word-originated Unicode characters in CSV imports), not by OS or version.

## Severity: high

## Impact
Any user who imports CSV data containing non-ASCII characters (very common when data originates from Word or other rich-text tools) will experience crashes on manual save, leading to data loss anxiety and inability to reliably save work. The workaround (deleting imported tasks) is destructive.

## Recommended Fix
Investigate the manual save code path and compare its encoding/serialization logic to the auto-save path. Ensure the manual save writer uses UTF-8 encoding consistently. Specifically: (1) find where the toolbar Save handler serializes task list content, (2) check for hardcoded encoding assumptions (ASCII, latin-1, etc.), (3) align it with the auto-save encoding which already handles Unicode correctly. Also review the CSV import path to ensure it preserves encoding metadata so downstream writers know to expect Unicode.

## Proposed Test Case
Create a task list containing text with curly quotes (‘’“”), em-dashes (—), and other common Unicode characters. Perform a manual save via the toolbar button. Verify the save completes without error and that the file can be re-opened with all characters intact. Additionally, test with a CSV import containing these characters followed by a manual save.

## Information Gaps
- Exact error message in the crash dialog (reporter could only partially read it)
- Whether the issue affects all platforms or is platform-specific
- The specific file format used by the manual save path (JSON, XML, custom binary, etc.)
