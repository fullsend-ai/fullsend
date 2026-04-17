# Triage Summary

**Title:** Salesforce sync truncates phone country codes longer than 2 digits

## Problem
The nightly Salesforce-to-TaskFlow sync job is corrupting international phone numbers by truncating country codes to 2 digits. Numbers with 3+ digit country codes (e.g., Ireland +353, Antigua +1268) are stored with only the first 2 digits of the country code, while 1-2 digit codes (US +1, UK +44) are unaffected. Approximately 50-100 of ~2,000 customer records are impacted.

## Root Cause Hypothesis
The sync job or TaskFlow's phone number storage has a 2-character limit on the country code portion. This is most likely either: (a) a database column storing the country code is defined as VARCHAR(2) or CHAR(2), or (b) the sync job's parsing logic uses a regex or substring that assumes country codes are at most 2 digits. A recent change to the sync job, the phone field schema, or the parsing library likely introduced this constraint — the sync had been working previously.

## Reproduction Steps
  1. Ensure a Salesforce contact has a phone number with a 3+ digit country code (e.g., +353 1 234 5678 for Ireland)
  2. Run the nightly sync job (or trigger it manually)
  3. Check the resulting phone number in TaskFlow — the country code will be truncated to 2 digits (e.g., +35 1 234 5678)
  4. Repeat with a +1 or +44 number to confirm those sync correctly

## Environment
Salesforce-to-TaskFlow nightly sync job. Problem started approximately 2 weeks ago. Affects international numbers with 3+ digit country codes (ITU-T E.164 codes like +353, +1268, +880, etc.).

## Severity: high

## Impact
Support team cannot reach affected customers by phone. Estimated 50-100 records corrupted out of ~2,000. All customers with 3+ digit country codes are affected, meaning international customers outside NANP (+1) and a few major countries are unreachable. Data integrity issue that worsens with each nightly sync if new records are added.

## Recommended Fix
1. Inspect the sync job's phone number parsing — look for a field, column, or regex that limits the country code to 2 characters. Check for recent changes (~2 weeks ago) to the sync job, schema migrations, or phone-field validation. 2. Fix the constraint to support country codes up to 4 digits (the ITU-T maximum). 3. Re-sync affected records from Salesforce to restore correct phone numbers. 4. Add a validation check or integration test that syncs a phone number with a 3+ digit country code.

## Proposed Test Case
Sync a contact from Salesforce with phone number +353 1 234 5678 (Ireland) and assert that TaskFlow stores exactly +353 1 234 5678. Include additional cases for +1268 (4-digit), +44 (2-digit), and +1 (1-digit) to cover all country code lengths.

## Information Gaps
- Exact date the problem started (reporter estimated ~2 weeks ago)
- Whether any changes were deployed to the sync job or phone field schema around that time
- Whether already-corrupted records are overwritten on each sync or only new/modified records are affected
