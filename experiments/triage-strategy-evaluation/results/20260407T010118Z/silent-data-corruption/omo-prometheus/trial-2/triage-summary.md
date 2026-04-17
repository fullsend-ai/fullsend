# Triage Summary

**Title:** Nightly Salesforce sync truncates international phone country codes to 2 digits

## Problem
The nightly Salesforce-to-TaskFlow sync is truncating phone number country codes longer than 2 digits. Country codes like +353 (Ireland, 3 digits) become +35, and +1268 (Antigua, 4 digits) become +12. Numbers with 1-2 digit country codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of ~2,000 customer records are affected. The corruption is ongoing — manually corrected numbers are re-corrupted by the next sync run.

## Root Cause Hypothesis
A code change deployed approximately 2-3 weeks ago to 'improve international phone number handling' in the custom Salesforce sync integration is capping country codes at 2 digits during parsing or formatting. The logic likely assumes all country codes are 1-2 digits (true for US/UK) and discards remaining digits, rather than consulting the ITU E.164 variable-length country code table (1-3 digits).

## Reproduction Steps
  1. Identify a customer record in Salesforce with a country code longer than 2 digits (e.g., +353 for Ireland)
  2. Ensure the phone number is correct in Salesforce (e.g., +353 1 234 5678)
  3. Wait for the nightly sync to run (or trigger it manually)
  4. Check the same record in TaskFlow — the country code will be truncated to 2 digits (e.g., +35 1 234 5678)

## Environment
Custom-built Salesforce-to-TaskFlow sync integration, runs nightly. Change was made by a developer likely named Dave, deployed approximately 2-3 weeks ago.

## Severity: high

## Impact
50-100 customer records have corrupted phone numbers. Support team cannot reach affected customers by phone. Manual corrections are overwritten nightly, so the issue compounds and blocks remediation. Affected customers are those in countries with 3+ digit country codes (much of Africa, Caribbean, parts of Europe and Asia-Pacific).

## Recommended Fix
1. Check git history from ~2-3 weeks ago for changes to phone number parsing/formatting in the sync integration (likely authored by Dave). 2. Fix the country code parsing to handle variable-length codes per E.164 (1-3 digits) — consider using a library like libphonenumber rather than hand-rolling parsing. 3. After deploying the fix, run a one-time re-sync or corrective script to restore the truncated numbers from Salesforce (the source data is still correct). 4. Add validation to the sync that flags or rejects phone numbers that change length unexpectedly, as a guardrail against future regressions.

## Proposed Test Case
Unit test the phone number parsing function with country codes of varying lengths: +1 (1 digit, US), +44 (2 digits, UK), +353 (3 digits, Ireland), +1268 (4 digits, Antigua). Assert that the full country code and subscriber number are preserved in each case. Additionally, add an integration test that syncs a Salesforce record with a 3-digit country code and verifies the TaskFlow record matches exactly.

## Information Gaps
- The exact commit or PR that introduced the change (developer team can find this in git history)
- Whether phone numbers with other formatting issues exist beyond country code truncation (e.g., extension handling, special characters)
- The exact date the corruption started (sync logs or record timestamps should reveal this)
