# Triage Summary

**Title:** Nightly Salesforce import truncates phone country codes to 2 characters

## Problem
Customer phone numbers with country codes longer than 2 digits (e.g., +353 for Ireland, +1268 for Antigua) are being truncated to 2 characters during the nightly Salesforce-to-TaskFlow import. Country codes that are already 1–2 digits (+1, +44) are unaffected. Approximately 50–100 of ~2,000 customer records are impacted. The data remains correct in Salesforce.

## Root Cause Hypothesis
A change introduced approximately two weeks ago in the Salesforce import pipeline is truncating the country code portion of phone numbers to 2 characters. This could be a schema change (e.g., a column defined as VARCHAR(2) or CHAR(2) for the country code), a parsing change that takes only the first 2 characters after the '+', or a validation/formatting change that assumes all country codes are at most 2 digits.

## Reproduction Steps
  1. Identify a Salesforce customer record with a 3+ digit country code (e.g., +353 for Ireland)
  2. Verify the phone number is correct in Salesforce
  3. Wait for or manually trigger the nightly import
  4. Check the same customer record in TaskFlow — the country code should be truncated to 2 digits (e.g., +35 instead of +353)

## Environment
TaskFlow production environment with nightly data sync from Salesforce CRM. Issue began approximately two weeks ago (late March 2026).

## Severity: high

## Impact
Support team cannot reach customers in countries with 3+ digit country codes. Approximately 50–100 records affected. Data corruption is ongoing — each nightly import re-corrupts any manually corrected numbers.

## Recommended Fix
1. Check git history on the Salesforce import code for changes made ~2 weeks ago. 2. Look for any field-length constraint, substring operation, or format mask that limits the country code to 2 characters. 3. Fix the truncation so country codes of any valid length (1–3 digits per E.164) are preserved. 4. Re-run the import or write a one-time repair script to re-sync affected records from Salesforce.

## Proposed Test Case
Import a customer record with a 3-digit country code (e.g., +353 1 234 5678 for Ireland) and verify the full number, including all country code digits, is stored correctly in TaskFlow. Include edge cases for 1-digit (+1), 2-digit (+44), and 3-digit (+353) country codes.

## Information Gaps
- Exact date the corruption started (reporter estimates ~2 weeks ago)
- Whether the nightly import overwrites previously correct values on every run (likely, which means manual corrections would be lost)
- The specific technology stack of the import pipeline (ETL tool, custom script, etc.)
