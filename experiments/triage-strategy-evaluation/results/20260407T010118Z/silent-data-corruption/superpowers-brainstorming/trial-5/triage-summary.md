# Triage Summary

**Title:** Salesforce sync truncates international phone country codes to 2 digits

## Problem
The nightly Salesforce-to-TaskFlow sync is corrupting international phone numbers by truncating country codes longer than 2 digits. Approximately 50-100 of 2,000 customer records are affected. The data in Salesforce remains correct; the corruption occurs during the sync into TaskFlow. This started approximately two weeks ago.

## Root Cause Hypothesis
A recent change in the sync pipeline — likely a schema change, field-length constraint, or phone number parsing/normalization update — is truncating the country code portion of phone numbers to a maximum of 2 characters after the '+' sign. Numbers with 1- or 2-digit country codes (US: +1, UK: +44) pass through unscathed, while 3+ digit codes (Ireland: +353, Antigua: +1268) are clipped.

## Reproduction Steps
  1. Identify a customer record with a country code longer than 2 digits in Salesforce (e.g., an Irish number starting with +353)
  2. Run or wait for the nightly sync job
  3. Compare the phone number in TaskFlow against the Salesforce source
  4. Observe that the country code is truncated to 2 digits (e.g., +353 → +35)

## Environment
Salesforce CRM → TaskFlow nightly API sync job. Issue began approximately two weeks ago. Affects international numbers with 3+ digit country codes.

## Severity: high

## Impact
Support team cannot reach affected international customers by phone. Approximately 50-100 records have corrupted contact data, and the nightly sync continues to re-corrupt any manual fixes.

## Recommended Fix
1. Review the sync job code and its recent changes (last 2-3 weeks) for any modification to phone number parsing, storage schema, or field length constraints. 2. Look specifically at how the country code portion is extracted or stored — check for a VARCHAR/column length change, a regex that assumes ≤2 digit country codes, or a new normalization step. 3. After fixing the sync logic, re-sync affected records from Salesforce to restore correct phone numbers.

## Proposed Test Case
Create test records in the sync pipeline with phone numbers using 1-digit (+1), 2-digit (+44), 3-digit (+353), and 4-digit (+1268) country codes. Verify all pass through the sync with their full country codes intact.

## Information Gaps
- Exact date the sync job or TaskFlow was last updated (would help pinpoint the commit but developer can check git history)
- Whether the sync job is custom-built or uses a TaskFlow-provided integration (developer can determine this from the codebase)
