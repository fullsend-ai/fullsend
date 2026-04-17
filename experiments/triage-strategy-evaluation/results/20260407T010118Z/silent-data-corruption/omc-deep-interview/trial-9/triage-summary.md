# Triage Summary

**Title:** Nightly Salesforce import truncates phone country codes longer than 2 digits

## Problem
Customer phone numbers imported from Salesforce via the nightly sync are having their country codes truncated to exactly 2 digits. Numbers with 1- or 2-digit country codes (e.g., +1 US, +44 UK) are unaffected, but numbers with 3- or 4-digit country codes (e.g., +353 Ireland, +1268 Antigua) are corrupted. The local portion of the number after the country code is preserved correctly. The issue is ongoing — manually corrected numbers are re-corrupted on the next import cycle.

## Root Cause Hypothesis
A code change made approximately 2 weeks ago to support international phone number formats introduced a bug that hard-codes or truncates the country code field to a maximum of 2 characters. This is likely in the phone number parsing/normalization logic applied during the Salesforce import pipeline. A developer reportedly made changes to 'how phone numbers are processed' around the time the issue began.

## Reproduction Steps
  1. Identify the nightly Salesforce import job/process
  2. Ensure a Salesforce record exists with a phone number using a country code longer than 2 digits (e.g., +353 1 234 5678 for Ireland)
  3. Run the import process (or wait for the nightly sync)
  4. Check the imported record in TaskFlow — the country code should be truncated to 2 digits (+35 1 234 5678)

## Environment
Production environment. Nightly Salesforce-to-TaskFlow sync process. Approximately 2,000 customer records total, 50-100 affected.

## Severity: high

## Impact
Support team cannot reach affected customers by phone. Data corruption is ongoing and overwrites manual corrections daily. Affects all customers with country codes longer than 2 digits (international customers outside the NANP +1 zone and countries with 2-digit codes).

## Recommended Fix
Search recent commits (approximately 2 weeks old) related to phone number parsing, formatting, or international number support in the Salesforce import pipeline. Look for a hard-coded length limit, substring operation, or field width constraint on the country code portion — likely something like `country_code[:2]` or a VARCHAR(2) column or similar 2-character truncation. Fix the parsing logic to accommodate country codes of 1-4 digits per the ITU E.164 standard. After fixing, run a one-time re-import or correction of the ~50-100 affected records.

## Proposed Test Case
Unit test the phone number parsing function with country codes of varying lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4 digits). Assert that the full country code is preserved in each case and the complete E.164 number is stored without truncation.

## Information Gaps
- Exact commit or PR that introduced the phone number processing change
- Which specific file/module contains the parsing logic
- Whether the truncation happens in application code or at the database schema level (e.g., a column width constraint)
