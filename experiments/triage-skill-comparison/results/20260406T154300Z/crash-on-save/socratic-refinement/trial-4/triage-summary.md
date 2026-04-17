# Triage Summary

**Title:** App crashes on save with large task lists created via CSV import

## Problem
Clicking the Save button in the toolbar causes the app to crash when the task list contains approximately 200 tasks that were imported from a CSV file. Saving works correctly with smaller lists (30-40 tasks) and worked correctly before the CSV import feature was used.

## Root Cause Hypothesis
The save operation likely has a performance or memory issue that manifests at scale. The CSV import may introduce data volume or payload size that exceeds a buffer, timeout, or memory limit in the save path. A secondary hypothesis is that the CSV import produces malformed or oversized field data (e.g., unclosed strings, unexpected characters) that corrupts serialization at scale but not in small batches.

## Reproduction Steps
  1. Create or open a task list in TaskFlow
  2. Import approximately 200 tasks from a CSV file
  3. Click the Save button in the toolbar
  4. Observe the app crash

## Environment
Not specified — reporter did not mention OS, browser, or app version. Reproduction should be attempted on standard environments first.

## Severity: high

## Impact
Users who import large datasets via CSV lose all unsaved work when the app crashes on save. This is a data-loss scenario that directly blocks a core workflow (CSV import + save).

## Recommended Fix
Investigate the save handler for issues at scale: check for synchronous serialization of the full task list that could blow a stack or memory limit, missing pagination or chunking in the save payload, or timeout issues on the backend. Also inspect the CSV import path to verify imported records are well-formed — check for excessively long fields, special characters, or encoding issues that could break serialization. Add a size-based stress test for the save path.

## Proposed Test Case
Import a CSV with 200+ tasks of representative data and verify that clicking Save completes without crashing. Additionally, test with intentionally malformed CSV data (special characters, very long fields, empty fields) at the 200-task scale to ensure the save path is resilient.

## Information Gaps
- Exact error message or crash log not provided
- Unknown whether the crash is client-side (UI freeze/JS error) or server-side (HTTP 500, timeout)
- App version, OS, and browser not specified
- CSV file contents and structure not examined — unclear if specific rows contain problematic data
- Exact threshold between 30-40 (works) and 200 (crashes) not pinpointed
