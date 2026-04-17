# Triage Summary

**Title:** International phone numbers truncated during Salesforce nightly sync — longer country codes clipped

## Problem
Customer phone numbers with longer country codes (3+ digits, e.g. +353 for Ireland, +1268 for Antigua) are being truncated during the nightly batch import from Salesforce into TaskFlow. Short country codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of 2,000 customer records are corrupted. The data is correct in Salesforce, confirming the corruption occurs on the TaskFlow side of the sync.

## Root Cause Hypothesis
A change introduced approximately two weeks ago — likely in TaskFlow v2.3.x, a database migration, or a sync field mapping update — is truncating the phone number field or its country code component. The most probable causes are: (1) a database column width or type change that limits the phone/country-code field length, (2) a parsing/normalization change in the sync importer that incorrectly handles country codes longer than 2 digits, or (3) a validation rule that trims phone numbers exceeding an assumed max length.

## Reproduction Steps
  1. Set up a Salesforce test record with a phone number using a 3+ digit country code (e.g. +353 1 234 5678 for Ireland)
  2. Run the nightly Salesforce-to-TaskFlow batch sync (or trigger it manually)
  3. Check the imported record in TaskFlow — the country code should appear truncated (e.g. +35 instead of +353)
  4. Compare with a +1 (US) number synced in the same batch — it should be unaffected

## Environment
TaskFlow v2.3.1, nightly batch import from Salesforce, issue started approximately two weeks ago

## Severity: high

## Impact
50-100 international customer contact records have corrupted phone numbers. Support team cannot reach affected customers by phone. Ongoing — each nightly sync likely re-corrupts any manually corrected numbers.

## Recommended Fix
1. Check TaskFlow release notes and deployment history for changes made ~2 weeks ago, especially to the phone number field, Salesforce sync importer, or database schema. 2. Inspect the phone number column definition in the database for any recent type or length changes. 3. Review the sync code's phone number parsing/normalization logic for assumptions about country code length (e.g. hardcoded 2-digit max). 4. Once the truncation cause is fixed, re-sync affected records from Salesforce to restore correct numbers. 5. Consider adding a validation check that flags phone numbers that change during sync as a safeguard.

## Proposed Test Case
Unit test the phone number import/normalization function with country codes of varying lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4 digits). Assert that the full country code and number are preserved in each case. Additionally, add an integration test that syncs a batch containing mixed country code lengths and verifies no truncation occurs.

## Information Gaps
- Exact TaskFlow version change history — whether v2.3.1 was deployed ~2 weeks ago or earlier
- Whether any database migration ran around that time
- The exact cutoff point — whether all 3+ digit country codes are affected or only those above a certain length
- Whether manually corrected numbers get re-corrupted on the next sync cycle
