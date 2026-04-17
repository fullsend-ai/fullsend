# Triage Summary

**Title:** Phone country codes longer than 2 digits truncated during Salesforce batch import

## Problem
The built-in Salesforce integration's nightly batch import is truncating phone number country codes to a maximum of 2 digits. Numbers with 1- or 2-digit country codes (+1, +44) are unaffected, but 3-digit codes like +353 (Ireland) become +35 and 4-digit codes like +1268 (Antigua) become +12. An estimated 50-100 of ~2,000 customer records are affected.

## Root Cause Hypothesis
A recent change (likely in TaskFlow v2.3.x or a migration applied around 2 weeks ago) introduced a field length constraint or parsing bug that limits the country code portion of international phone numbers to 2 characters. This could be a database column width change, a validation regex update, or a parsing function that splits on the wrong boundary when extracting the country code during import normalization.

## Reproduction Steps
  1. Set up the built-in Salesforce integration via the TaskFlow admin panel
  2. Ensure Salesforce contains contact records with phone numbers using 3+ digit country codes (e.g., +353 1 234 5678 for Ireland)
  3. Run the nightly batch import (or trigger it manually if possible)
  4. Check the imported phone numbers in TaskFlow — country codes longer than 2 digits will be truncated to 2 digits

## Environment
TaskFlow v2.3.1, built-in Salesforce integration configured through admin panel, nightly batch import schedule

## Severity: high

## Impact
50-100 customer contact records have corrupted phone numbers, preventing support staff from reaching customers. Data corruption is ongoing with each nightly import, so the number of affected records will grow. Affects any organization using the Salesforce integration with international contacts outside +1/+44 regions.

## Recommended Fix
1. Check recent changes (last ~2 weeks) to the phone number parsing/normalization code in the Salesforce import pipeline, especially any country code extraction logic. 2. Look for database schema migrations that may have altered column widths on phone-related fields. 3. Review any validation regex changes that constrain country code length. 4. After fixing, provide a data remediation path — re-import affected records or re-sync from Salesforce to restore correct numbers.

## Proposed Test Case
Import phone numbers with country codes of varying lengths (+1, +44, +353, +1268, +880) via the Salesforce integration and verify all digits are preserved exactly. Include edge cases: 1-digit (+1), 2-digit (+44), 3-digit (+353), and 4-digit (+1268) country codes.

## Information Gaps
- Exact TaskFlow release or deployment change that occurred ~2 weeks ago that may have introduced the regression
- Whether the truncation happens at import time (data written incorrectly) or at display time (data stored correctly but rendered incorrectly)
- Import logs from the Salesforce integration that might show warnings or errors during processing
