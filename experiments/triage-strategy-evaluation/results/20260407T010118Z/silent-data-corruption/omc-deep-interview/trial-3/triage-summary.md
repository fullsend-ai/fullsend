# Triage Summary

**Title:** Phone number country codes truncated to 2 digits during Salesforce nightly import

## Problem
Customer phone numbers with country codes longer than 2 digits (e.g., +353 for Ireland, +1268 for Antigua) are being truncated during the nightly Salesforce-to-TaskFlow batch import. Numbers with 1-2 digit country codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of 2,000 customer records are corrupted.

## Root Cause Hypothesis
A recent change to TaskFlow's phone number import/parsing logic (made approximately two weeks ago) is truncating the country code portion of international phone numbers to a maximum of 2 digits. The truncation pattern suggests a field length constraint, substring operation, or regex pattern that assumes country codes are at most 2 digits — when in fact they can be 1-3 digits per the E.164 standard.

## Reproduction Steps
  1. Set up a Salesforce test record with a phone number using a 3-digit country code (e.g., +353 1 234 5678 for Ireland)
  2. Run the nightly batch import process (or trigger it manually)
  3. Observe the imported phone number in TaskFlow — expect the country code to be truncated to 2 digits (e.g., +35 1 234 5678)
  4. Compare with a record using a 1-2 digit country code (e.g., +1 or +44) to confirm those import correctly

## Environment
TaskFlow production environment with nightly batch import from Salesforce CRM. Issue began approximately two weeks ago (~late March 2026). The sync is currently still active and re-corrupting data nightly.

## Severity: high

## Impact
50-100 customer contact records have incorrect phone numbers. Support team is unable to reach affected customers by phone. The nightly sync is actively re-corrupting any manually corrected records. Affected customers are those in countries with 3-digit country codes (e.g., Ireland +353, Antigua +1268, and likely others). US and UK customers are unaffected.

## Recommended Fix
1. Review recent changes (last ~2 weeks) to the phone number import/parsing logic in the Salesforce sync pipeline. Look for substring operations, regex patterns, or field length constraints that limit country codes to 2 digits. 2. Fix the parsing to support 1-3 digit country codes per the E.164 standard. 3. After fixing, re-run the import to restore correct numbers from Salesforce (source data confirmed intact). 4. Consider adding validation that flags phone numbers that change during import as a safeguard.

## Proposed Test Case
Unit test the phone number import parser with numbers covering all country code lengths: 1-digit (+1 US), 2-digit (+44 UK), and 3-digit (+353 Ireland, +1268 Antigua, +880 Bangladesh). Assert that the full country code is preserved in each case. Additionally, add an integration test that imports a batch containing mixed country code lengths and verifies all are stored correctly.

## Information Gaps
- Exact code change made ~2 weeks ago to phone number handling (reporter does not manage this; dev team would know)
- Whether the issue affects numbers edited/added via the TaskFlow UI or only the batch import path
- Complete list of affected country codes beyond the two examples provided
