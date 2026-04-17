# Triage Summary

**Title:** Toolbar Save crashes with encoding error when task list contains non-ASCII characters from CSV import

## Problem
The application force-quits when the user clicks Save in the toolbar. A brief error dialog mentioning 'encoding' flashes before the app closes. Auto-save continues to work correctly. The issue began after the user imported ~200 tasks from a CSV file that originated from a Microsoft Word document, likely containing smart quotes, em-dashes, and other extended characters.

## Root Cause Hypothesis
The toolbar Save code path uses a different serialization or encoding method than auto-save. It likely writes with a strict encoding (e.g., ASCII) or fails to handle non-ASCII characters (smart quotes U+2018/U+2019, em-dashes U+2014, etc.), causing an unhandled encoding exception that crashes the app. Auto-save probably uses a more permissive encoding (e.g., UTF-8) or a different serialization library that handles these characters gracefully.

## Reproduction Steps
  1. Create or import a task with smart quotes (e.g., ‘example’) or em-dashes (—) in the title or description
  2. Click the Save button in the toolbar
  3. Observe the app crash with a brief encoding error dialog

## Environment
User has been using the app for several months. Recently imported ~200 tasks from a CSV file originally created from a Word document. Specific OS and app version not confirmed.

## Severity: high

## Impact
User cannot manually save their work at all. The toolbar Save button is completely non-functional with their current dataset. Data loss risk is mitigated only by auto-save working, but user has no way to force a save.

## Recommended Fix
Compare the encoding and serialization logic between the toolbar Save handler and the auto-save handler. The toolbar Save path likely needs to use UTF-8 encoding (or match whatever encoding auto-save uses). Additionally, the save function should catch encoding errors gracefully rather than crashing the app — surface a user-readable error identifying the problematic content instead of force-quitting.

## Proposed Test Case
Create a task list containing various non-ASCII characters (smart quotes, em-dashes, accented characters, emoji) and verify that toolbar Save completes without error. Include a regression test that saves a task with content copied from a Word document and asserts no encoding exception is thrown.

## Information Gaps
- Exact OS and app version
- Exact text of the flashing error dialog
- Whether the issue reproduces with a single task containing special characters (isolated from the 200-task dataset)
