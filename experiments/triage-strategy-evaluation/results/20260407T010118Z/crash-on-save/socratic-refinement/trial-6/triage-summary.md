# Triage Summary

**Title:** App crashes on save after importing CSV containing Word-originated special characters

## Problem
The application crashes (window immediately disappears with a briefly-flashed error dialog) whenever the user attempts to save their task list. The user has approximately 200 tasks, ~150 of which were imported from a CSV file. The crash began immediately after that import and is 100% reproducible on every save attempt.

## Root Cause Hypothesis
The CSV import introduced text containing Word-style special characters (smart/curly quotes, em dashes, ellipses, or other non-ASCII Unicode characters). The save/serialization routine likely has an unhandled encoding error or character-escaping bug that throws an exception when processing these characters, and the crash handler fails to catch it gracefully — causing the window to close instead of displaying a usable error.

## Reproduction Steps
  1. Create a CSV file with task names containing Word-style special characters (curly quotes, em dashes, ellipses, etc.)
  2. Import the CSV into TaskFlow using the CSV import feature
  3. Attempt to save the task list
  4. Observe: application window closes abruptly with a briefly-flashed error dialog

## Environment
Not confirmed — reporter did not specify OS or app version. The crash behavior (window disappearing with flashed error) suggests a desktop application build.

## Severity: critical

## Impact
User is completely unable to save any work. Data loss occurs on every save attempt. The user has been blocked for multiple days and cannot use the application productively.

## Recommended Fix
1. Inspect the save/serialization code path for character encoding assumptions (e.g., ASCII-only handling, unescaped special characters in JSON/XML/SQL output). 2. Add proper Unicode handling and escaping during serialization. 3. Wrap the save operation in error handling that surfaces a readable error message rather than crashing. 4. Consider validating imported data at CSV import time to sanitize or warn about problematic characters.

## Proposed Test Case
Import a CSV containing task names with Word-style Unicode characters (e.g., curly quotes “”, em dash —, ellipsis …, accented characters) and verify that saving completes successfully without crashing. Additionally, test with a large number of such tasks (150+) to rule out a volume-related interaction.

## Information Gaps
- Exact OS and application version
- The actual CSV file contents (to identify the specific offending characters)
- The exact error message from the flashed dialog (could be captured via screen recording or logs)
