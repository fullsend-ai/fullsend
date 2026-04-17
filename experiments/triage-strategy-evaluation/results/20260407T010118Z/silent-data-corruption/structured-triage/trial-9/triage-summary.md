# Triage Summary

**Title:** International phone numbers truncated during Salesforce sync since v2.3.1 update

## Problem
Customer phone numbers with international country codes (notably 3-digit codes like Ireland's +353) are being truncated during the nightly Salesforce CRM sync. Approximately 50-100 of ~2,000 customer records are affected. US (+1) and UK (+44) numbers appear unaffected, suggesting the issue is specific to certain country code lengths or formats.

## Root Cause Hypothesis
A change introduced in or around the TaskFlow v2.3.1 release — likely related to phone number formatting or international format support — is truncating country codes during the Salesforce import/sync pipeline. The most probable cause is a parsing or validation function that assumes a maximum country code length of 2 digits, clipping 3-digit codes like +353 to +35. Alternatively, a field length constraint or regex pattern may have been tightened incorrectly.

## Reproduction Steps
  1. Set up a Salesforce sync connection with customer records containing international phone numbers with 3-digit country codes (e.g., +353 for Ireland, +370 for Lithuania)
  2. Include control records with 1-digit (+1 US) and 2-digit (+44 UK) country codes
  3. Run the nightly sync (or trigger a manual sync if supported)
  4. Compare the imported phone numbers in TaskFlow against the Salesforce source records
  5. Verify that 3-digit country codes are truncated while 1- and 2-digit codes are preserved correctly

## Environment
TaskFlow v2.3.1, Salesforce CRM integration via nightly sync

## Severity: high

## Impact
Approximately 50-100 customer records have corrupted phone numbers, preventing the support team from reaching those customers. Ongoing nightly syncs continue to introduce or perpetuate the corruption. Affected customers appear to be those with international numbers from countries with 3-digit country codes.

## Recommended Fix
1. Check the v2.3.1 changelog/diff for changes to phone number parsing, formatting, or international format handling in the Salesforce sync/import code path. 2. Inspect the phone number normalization or validation function for assumptions about country code length (likely a substring, regex, or field-length issue). 3. Fix the parsing to handle all valid country code lengths (1-3 digits per ITU E.164). 4. Run a remediation script to re-sync or repair the ~50-100 affected records from Salesforce.

## Proposed Test Case
Unit test: pass phone numbers with 1-digit (+1), 2-digit (+44), and 3-digit (+353, +370) country codes through the sync/import phone normalization function and assert all country codes are preserved in full. Integration test: run a Salesforce sync with test records covering all country code lengths and verify round-trip fidelity.

## Information Gaps
- Exact examples of truncated vs. expected phone numbers (reporter did not have them on hand)
- Sync job logs or error output that might show warnings during import
- The specific v2.3.1 changelog entry the reporter vaguely recalls seeing
- Whether the issue affects all 3-digit country codes or only specific ones
