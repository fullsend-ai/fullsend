# Triage Summary

**Title:** Built-in Salesforce sync truncates international phone country codes longer than 2 digits (v2.3.1)

## Problem
Customer phone numbers synced from Salesforce via TaskFlow's built-in integration are having their country codes truncated to exactly 2 characters. Numbers with 1- or 2-digit country codes (US +1, UK +44) are unaffected, but 3+ digit codes (Ireland +353 → +35, Antigua +1268 → +12) are silently corrupted. Approximately 50-100 of ~2,000 customer records are affected.

## Root Cause Hypothesis
A code change in or around v2.3.1 (deployed roughly 2 weeks ago) likely introduced a fixed-width field, substring operation, or validation rule that limits the country code portion of phone numbers to 2 characters during the Salesforce sync import/parsing step. This could be a database column width change, a regex pattern with a {1,2} quantifier on the country code group, or a phone number normalization library update.

## Reproduction Steps
  1. Set up the built-in Salesforce integration on TaskFlow v2.3.1
  2. Ensure Salesforce contains contacts with international phone numbers using 3+ digit country codes (e.g., +353 for Ireland, +1268 for Antigua)
  3. Run the nightly sync (or trigger a manual sync if available)
  4. Inspect the synced phone numbers in TaskFlow — country codes should be truncated to 2 digits

## Environment
TaskFlow v2.3.1, built-in Salesforce integration, nightly sync schedule. Issue began approximately 2 weeks ago with no changes on the customer's Salesforce side.

## Severity: high

## Impact
Data integrity issue affecting customer contact records. Support teams are unable to reach customers with 3+ digit country codes. Affects any organization using the Salesforce sync with international contacts — estimated 50-100 records for this reporter, but likely affects all users of the integration with similar data.

## Recommended Fix
1. Check git history for changes to the Salesforce sync / phone number parsing code in the 2-week window before this report. 2. Look for field length constraints, regex patterns, or normalization logic applied to the country code portion of E.164 phone numbers. 3. Verify the database schema for the phone number column has not been altered. 4. Fix the parser to handle country codes of 1-4 digits per the ITU E.164 standard. 5. Write a data migration to re-sync affected records from Salesforce to restore correct numbers.

## Proposed Test Case
Unit test that syncs phone numbers with country codes of varying lengths (+1, +44, +353, +1268, +992) through the Salesforce import path and asserts all digits are preserved in the stored result. Include an integration test that round-trips these numbers through a mock Salesforce sync and verifies no truncation occurs.

## Information Gaps
- Exact date the issue started (reporter estimated ~2 weeks ago but is unsure)
- Whether a manual re-sync or single-record sync reproduces the issue, or only the nightly batch
- Server-side sync logs that might show warnings or errors during phone number processing
