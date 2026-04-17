# Triage Summary

**Title:** Manual save crashes with encoding error when project contains non-ASCII characters imported from CSV

## Problem
Clicking the Save button causes the app to display a brief encoding-related dialog and then crash (immediate close, not a freeze). This only affects manual save — auto-save works correctly. The issue is triggered by tasks imported from a CSV file that contained special Unicode characters (curly quotes, em-dashes) originating from a Word document.

## Root Cause Hypothesis
The manual save code path likely uses a different text encoding or serialization method than auto-save. When it encounters non-ASCII characters (smart/curly quotes, em-dashes, and potentially other Windows-1252 or Unicode characters copied from Word), it hits an unhandled encoding error that causes the application to crash. The brief 'encoding' dialog suggests the error is partially caught but not gracefully handled. Auto-save likely uses a different serializer or encoding setting that handles these characters correctly.

## Reproduction Steps
  1. Create a CSV file containing task names with curly quotes (\u2018, \u2019, \u201C, \u201D) and em-dashes (\u2014) — copying text from a Word document is an easy way to get these characters
  2. Import the CSV into TaskFlow (import enough tasks to have a sizable project, ~200 tasks were reported)
  3. Click the Save button in the toolbar
  4. Observe: a dialog mentioning 'encoding' flashes briefly, then the app closes/crashes

## Environment
Not specified (likely desktop app). Issue is content-dependent rather than platform-dependent — any environment importing CSV with non-ASCII characters should reproduce it.

## Severity: high

## Impact
Any user who imports tasks from CSV files containing non-ASCII characters (common when data originates from Word or other rich-text sources) will be unable to manually save their project. While auto-save provides a partial workaround, users risk losing work if they rely on manual save, and the crash erodes trust in the application. This likely affects many users since CSV import from Word/Excel is a common workflow.

## Recommended Fix
1. Investigate the manual save code path's text encoding — compare it to auto-save to identify the divergence. 2. Ensure manual save uses UTF-8 encoding (or matches whatever encoding auto-save uses successfully). 3. Add proper error handling around the encoding step so that even if an unexpected character is encountered, the app shows a meaningful error instead of crashing. 4. Consider normalizing or sanitizing imported text at CSV import time (e.g., replacing curly quotes with straight quotes, em-dashes with hyphens) as defense-in-depth.

## Proposed Test Case
Create a project with tasks containing various non-ASCII characters (curly quotes, em-dashes, accented characters, emoji) imported via CSV. Verify that manual save completes without error and that the saved file correctly preserves the original characters when reloaded.

## Information Gaps
- Exact platform/OS version not confirmed (though likely not relevant to root cause)
- Exact app version not provided
- Full error message from the encoding dialog not captured (only partial recall)
- Whether the issue also affects other special characters beyond curly quotes and em-dashes
