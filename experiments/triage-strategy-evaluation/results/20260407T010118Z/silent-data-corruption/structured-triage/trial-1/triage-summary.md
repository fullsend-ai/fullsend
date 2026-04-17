# Triage Summary

**Title:** Nightly Salesforce import truncates phone country codes longer than 2 digits

## Problem
Customer phone numbers imported from Salesforce via the nightly batch sync are having their country codes truncated to exactly 2 digits. Numbers with 1- or 2-digit country codes (e.g., US +1, UK +44) are unaffected, but longer codes like Ireland (+353) and Antigua and Barbuda (+1268) are corrupted. An estimated 50-100 out of ~2,000 customer records are affected.

## Root Cause Hypothesis
A recent change (~2 weeks ago) to phone number processing or formatting logic likely introduced a hard limit or substring operation that caps the country code field at 2 characters. This could be a parsing regex, a database column width change, or a formatting function that assumes all country codes are at most 2 digits.

## Reproduction Steps
  1. Ensure a Salesforce record exists with a phone number whose country code is 3+ digits (e.g., +353 1 234 5678 for Ireland)
  2. Run the nightly batch import (or trigger it manually)
  3. Check the imported record in TaskFlow — the country code should be truncated to 2 digits (e.g., +35 1 234 5678)

## Environment
TaskFlow v2.3.1, nightly batch import from Salesforce CRM

## Severity: high

## Impact
50-100 customer contact records have corrupted phone numbers, preventing support staff from reaching those customers. Affected customers are those in countries with 3+ digit country codes. The corruption is ongoing with each nightly import, so corrected records would be re-corrupted.

## Recommended Fix
Review commits from approximately 2 weeks ago related to phone number processing or formatting. Look for changes that parse or store the country code portion of international phone numbers — specifically any logic that limits country code length to 2 characters (e.g., substring(0,2), a regex like /^\+(\d{1,2})/, or a database column resized to 2 characters). Fix the truncation, then run a one-time repair to re-import affected records from Salesforce.

## Proposed Test Case
Create a unit test for the phone number import/formatting function that passes numbers with 1-digit (+1), 2-digit (+44), 3-digit (+353), and 4-digit (+1268) country codes, and asserts all are stored without truncation.

## Information Gaps
- Exact commit or change made ~2 weeks ago to phone number processing (discoverable via git history)
- Whether the truncation occurs during import parsing, storage, or display (developer can check by inspecting the database directly)
- Import job logs from the past two weeks that might show warnings or errors
