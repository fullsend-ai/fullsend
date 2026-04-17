# Triage Summary

**Title:** Search filter inverted since v2.3.1 — 'active' filter returns archived tasks and vice versa

## Problem
Since the v2.3.1 update (approximately three days ago), the search status filter is inverted. When the default 'active tasks only' filter is applied, search returns archived tasks instead. When the user manually switches to 'show archived,' active tasks appear. This affects all users and all search queries.

## Root Cause Hypothesis
The v2.3.1 update likely introduced a boolean inversion or swapped enum/flag values in the search filter logic — the predicate that maps the UI filter selection (active vs. archived) to the backend query is reversed. This could be a negated condition, swapped constant values, or an inverted boolean parameter in the search API call.

## Reproduction Steps
  1. Log into TaskFlow web app (v2.3.1)
  2. Use the main search bar at the top of the app
  3. Type a search term (e.g., 'Q2 planning') and press Enter
  4. Observe that results include archived tasks and omit known active tasks
  5. Toggle the filter to 'show archived'
  6. Observe that active tasks now appear in the results

## Environment
TaskFlow v2.3.1, web app. Reproduced on Chrome/Windows 10 and Safari/macOS — browser-independent, likely a backend or shared frontend logic issue.

## Severity: high

## Impact
All users are affected. Every search returns wrong results by default, making the search feature effectively broken for normal use. Users must manually invert the filter as a workaround, which is non-obvious.

## Recommended Fix
Inspect the v2.3.1 diff for changes to search filter logic — look for inverted booleans, swapped enum values, or negated conditions in the code that maps the UI filter state (active/archived) to the search query parameters. Check both the frontend filter serialization and the backend query construction.

## Proposed Test Case
Create one active task and one archived task with the same keyword. Search with the default 'active' filter and assert the active task is returned and the archived task is not. Then switch to 'archived' filter and assert the reverse.

## Information Gaps
- Exact database or search index technology in use (e.g., Elasticsearch, PostgreSQL full-text) — useful for the developer but discoverable from the codebase
- Whether the issue also affects API-level search or only the UI-driven search path
