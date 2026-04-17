# Triage Summary

**Title:** Salesforce sync truncates international phone country codes longer than 2 digits

## Problem
The nightly Salesforce-to-TaskFlow customer sync job is truncating the country code portion of international phone numbers to 2 digits. Numbers with 1-2 digit country codes (+1 US, +44 UK) are unaffected, but longer country codes are mangled (e.g., +353 Ireland → +35, +1268 → +12). Approximately 50-100 of ~2,000 customer records are affected. The issue started roughly two weeks ago.

## Root Cause Hypothesis
A recent change (within the last ~2 weeks) to the sync job's phone number parsing or storage logic is truncating the country code field to 2 characters. This could be a schema change (e.g., country_code column width reduced), a regex/parsing change that extracts only the first 2 digits after '+', or a new formatting/validation step that incorrectly limits country code length.

## Reproduction Steps
  1. Identify a customer record in Salesforce with a country code longer than 2 digits (e.g., an Irish number starting with +353)
  2. Wait for the nightly sync to run (or trigger it manually)
  3. Compare the phone number in TaskFlow to the Salesforce source
  4. Observe that the country code is truncated to 2 digits in TaskFlow

## Environment
Nightly automated sync job pulling customer data from Salesforce CRM into TaskFlow via API integration

## Severity: high

## Impact
Support team cannot reach ~50-100 international customers by phone. Every nightly sync run likely re-corrupts any manually corrected numbers. Data integrity issue that erodes trust in the system.

## Recommended Fix
Review the sync job's commit/deploy history from the past 2-3 weeks for changes to phone number handling. Check for: (1) database schema changes to phone or country_code fields, (2) changes to phone number parsing logic (regex, string slicing, or library updates), (3) new validation or formatting steps. Fix the truncation, then run a one-time re-sync or correction job to repair the ~50-100 affected records from Salesforce source data.

## Proposed Test Case
Sync a test customer record with a 3+ digit country code (e.g., +353 1 234 5678 for Ireland) and verify the full number including complete country code is stored correctly in TaskFlow. Include edge cases: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1684 (4 digits).

## Information Gaps
- Exact date of the last known good sync (reporter estimated ~2 weeks ago)
- Whether a specific deployment or configuration change coincided with the onset
