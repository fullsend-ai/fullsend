# Triage Summary

**Title:** Salesforce sync truncates phone country codes to 2 digits, corrupting international numbers

## Problem
The nightly Salesforce-to-TaskFlow contact sync is mangling phone numbers for customers with country codes longer than 2 digits. Approximately 50-100 of ~2,000 customer records are affected. US (+1) and UK (+44) numbers are unaffected because their country codes are already ≤2 digits.

## Root Cause Hypothesis
A change introduced approximately 2 weeks ago in the phone number parsing or normalization logic within the Salesforce sync pipeline is truncating the country code portion of phone numbers to exactly 2 digits. The remaining digits are then concatenated without proper formatting. This is likely a regex, substring, or parsing library change that assumes all country codes are 1-2 digits.

## Reproduction Steps
  1. Identify a customer record in Salesforce with a country code of 3+ digits (e.g., Antigua +1268, Ireland +353)
  2. Verify the phone number is correct in Salesforce
  3. Wait for (or manually trigger) the nightly sync to TaskFlow
  4. Observe the phone number in TaskFlow — country code will be truncated to 2 digits and remaining digits concatenated

## Environment
TaskFlow instance with Salesforce CRM integration via nightly batch import. Regression began approximately 2 weeks ago (late March 2026). Specific TaskFlow version and deployment date unknown but correlatable via deployment logs.

## Severity: high

## Impact
Support team cannot reach ~50-100 international customers by phone. Data integrity issue affecting active customer contact records. The corruption recurs nightly since the sync overwrites TaskFlow data from Salesforce each run, meaning manual corrections would be overwritten until the root cause is fixed.

## Recommended Fix
1. Check deployment/commit history from ~2 weeks ago for changes to the Salesforce sync pipeline, specifically phone number parsing, normalization, or formatting logic. 2. Look for code that extracts or validates country codes — likely a regex, substring operation, or library call that assumes max 2-digit country codes. 3. Fix the parser to handle 1-4 digit country codes per the ITU-T E.164 standard. 4. Re-run the sync (or a targeted repair) to restore correct numbers from Salesforce for all affected records.

## Proposed Test Case
Create test phone numbers with country codes of varying lengths: 1 digit (+1), 2 digits (+44), 3 digits (+353), and 4 digits (+1268). Run them through the sync's phone normalization logic and assert the full country code and subscriber number are preserved in E.164 format.

## Information Gaps
- Exact deployment date or version that introduced the regression (obtainable from TaskFlow deployment logs)
- Whether the bug is in TaskFlow's sync ingestion code or in a shared phone-number parsing library
- Whether manually editing a phone number in TaskFlow's UI also triggers the truncation (would indicate the bug is in a shared normalization layer rather than sync-specific code)
