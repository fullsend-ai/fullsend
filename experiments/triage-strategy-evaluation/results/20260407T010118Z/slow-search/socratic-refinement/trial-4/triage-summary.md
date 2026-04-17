# Triage Summary

**Title:** Full-text description search freezes UI for 10-15s since v2.3 upgrade (title search unaffected)

## Problem
After upgrading from TaskFlow 2.2 to 2.3 approximately two weeks ago, keyword searches that scan task descriptions take 10-15 seconds and freeze the UI entirely, with visible CPU spike. The same searches that only match against task titles remain fast. The user has ~5,000 tasks accumulated over two years, with some containing very large descriptions (pasted meeting notes).

## Root Cause Hypothesis
The v2.3 update likely changed the full-text search implementation for task descriptions — most probably removed or broke a search index, switched from indexed/async search to brute-force synchronous scanning, or moved the search operation onto the main/UI thread. The CPU spike and UI freeze indicate a CPU-bound synchronous operation blocking the render loop while scanning large text fields across 5,000 records.

## Reproduction Steps
  1. Install TaskFlow 2.3
  2. Have a dataset with ~5,000 tasks, including tasks with large description fields (multi-paragraph text)
  3. Perform a keyword search using the search bar with full-text description search enabled
  4. Observe 10-15 second UI freeze with elevated CPU usage
  5. Compare: perform the same search restricted to title-only and observe it completes quickly

## Environment
TaskFlow 2.3 (upgraded from 2.2), work laptop (specific OS not confirmed), ~5,000 tasks with some very large description fields

## Severity: high

## Impact
Any user with a moderately large task database (~5,000+ tasks) using full-text description search is affected. The UI freeze makes the application appear unresponsive and disrupts core search workflows. Title-only search is a partial workaround but limits functionality.

## Recommended Fix
1. Diff the search implementation between v2.2 and v2.3, specifically for description-field search. 2. Check whether a full-text search index was dropped, altered, or is no longer being used. 3. Verify whether description search is running on the main/UI thread — if so, move it to a background thread or web worker. 4. Profile the search query against a dataset with ~5,000 records with large text fields. 5. Consider adding pagination or debouncing if not already present.

## Proposed Test Case
Create a test dataset with 5,000+ tasks where at least 10% have description fields >2,000 characters. Execute a full-text description keyword search and assert that results return within an acceptable threshold (e.g., <1 second) and that the UI/main thread is not blocked during execution.

## Information Gaps
- Exact OS and hardware specs of the reporter's work laptop
- Whether the issue reproduces on a clean 2.3 install (vs. upgrade path)
- Specific changelog or migration notes for the 2.2→2.3 upgrade that might reference search changes
