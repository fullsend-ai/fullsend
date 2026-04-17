# Triage Summary

**Title:** Nightly Salesforce sync truncates phone country codes longer than 2 digits

## Problem
Customer phone numbers with country codes longer than 2 digits (e.g. +353 Ireland, +1268 Antigua) are being truncated to 2-digit country codes during the nightly Salesforce-to-TaskFlow data sync. Approximately 50-100 of ~2,000 customer records are affected. Numbers with 1-2 digit country codes (+1 US, +44 UK) are unaffected. The source data in Salesforce remains correct.

## Root Cause Hypothesis
The phone number parsing or normalization logic in the Salesforce import pipeline is truncating country codes to exactly 2 digits. This likely involves a hard-coded assumption that country codes are at most 2 digits (or a fixed-width substring extraction like `number[0:3]` to capture '+' plus 2 digits). This may have been introduced by a code change approximately 2 weeks ago.

## Reproduction Steps
  1. Ensure a Salesforce test record exists with a phone number using a 3-digit country code (e.g. +353 1 234 5678 for Ireland)
  2. Ensure a Salesforce test record exists with a 1-digit country code (e.g. +1 555 123 4567 for US) as a control
  3. Run the nightly Salesforce sync process (or trigger it manually)
  4. Inspect the resulting phone numbers in TaskFlow — the 3-digit country code should be truncated to 2 digits while the 1-digit code remains correct
  5. Also test with a 4-digit code if possible (e.g. certain island nations) to confirm the pattern

## Environment
TaskFlow production environment with nightly Salesforce CRM integration. Phone numbers are formatted with country code and spaces (e.g. +353 1 234 5678) in Salesforce. Exact sync mechanism (API connector vs file export) unknown — check with customer's dev team.

## Severity: high

## Impact
Support team cannot reach ~50-100 customers due to corrupted phone numbers. Data corruption is likely recurring with each nightly sync, so manual corrections would be overwritten. This is an ongoing data integrity issue affecting customer communication capabilities.

## Recommended Fix
1. Locate the Salesforce sync/import code path that processes phone numbers. 2. Look for country code parsing logic — likely a hard-coded 2-digit assumption, fixed-width substring, or regex like `\+\d{2}`. 3. Check git history for changes to this code ~2 weeks ago. 4. Fix the parsing to handle variable-length country codes (1-3 digits per ITU-T E.164). 5. Consider using a phone number library (e.g. libphonenumber) for robust parsing. 6. After fixing, re-run the sync to repair the ~50-100 affected records from the correct Salesforce source data.

## Proposed Test Case
Unit test the phone number import/normalization function with country codes of varying lengths: +1 (1 digit, US), +44 (2 digits, UK), +353 (3 digits, Ireland), +1268 (4 digits, Antigua). Assert that all country codes are preserved in full. Additionally, add an integration test that syncs a batch of test records with mixed country code lengths and verifies none are truncated.

## Information Gaps
- Exact technical mechanism of the Salesforce sync (API connector, CSV export, middleware) — reporter directed us to their dev team
- Precise date the issue started and whether it correlates with a specific TaskFlow release or config change
- Whether manually corrected numbers are actually re-corrupted on the next sync cycle (reporter suspects yes but hasn't verified)
- Exact phone number format received by TaskFlow from the sync (E.164 vs formatted) — reporter unsure
