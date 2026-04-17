# Triage Summary

**Title:** Nightly Salesforce sync truncates phone country codes longer than 2 digits

## Problem
Customer phone numbers with country codes longer than 2 digits (e.g., +353 Ireland, +1268 Antigua) are being truncated to 2 characters during the nightly Salesforce-to-TaskFlow batch import. Numbers with 1- or 2-digit country codes (+1 US, +44 UK) are unaffected. The source data in Salesforce remains correct; corruption occurs on the TaskFlow side during import.

## Root Cause Hypothesis
A code change approximately two weeks ago — described as improving international phone number format support — likely introduced a parsing or formatting step that truncates the country code field to a maximum of 2 characters. This logic is applied during the nightly batch sync, so only synced records are affected.

## Reproduction Steps
  1. Identify the code change from ~2 weeks ago related to international phone number handling
  2. Set up a test customer record in Salesforce with a country code longer than 2 digits (e.g., +353 1 234 5678 for Ireland)
  3. Run the nightly Salesforce-to-TaskFlow sync (or invoke the sync function directly)
  4. Verify the phone number in TaskFlow — expect the country code to be truncated to 2 characters (+35 1 234 5678)
  5. Repeat with a 1-digit country code (+1) and confirm it is unaffected

## Environment
TaskFlow production environment with nightly batch import from Salesforce CRM. Approximately 2,000 customer records synced. Issue began ~2 weeks ago coinciding with a developer change to phone number handling.

## Severity: high

## Impact
Approximately 50-100 customer records have corrupted phone numbers. Support team cannot reach affected international customers. All customers with country codes of 3+ digits are potentially affected on each sync run, meaning the corruption is ongoing and may worsen as more records are touched.

## Recommended Fix
1. Locate the commit from ~2 weeks ago that modified phone number parsing/formatting in the sync pipeline. 2. Look for a hard-coded length limit, substring operation, or field width constraint on the country code portion (likely capping at 2 characters). 3. Fix the parsing to accommodate country codes of 1-3 digits per the E.164 standard. 4. Run a one-time remediation to re-sync affected records from Salesforce to restore correct phone numbers.

## Proposed Test Case
Unit test the phone number parsing/formatting function with country codes of varying lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4-digit area code treated as country code). Assert that all digits are preserved through the transformation. Add an integration test that syncs a record with a 3-digit country code and verifies the stored value matches the source.

## Information Gaps
- Exact commit or PR that changed phone number handling ~2 weeks ago
- Whether the truncation happens in a parsing function, a database column constraint, or a field mapping configuration
- Full count of affected records (reporter estimated 50-100 but hasn't done exhaustive audit)
