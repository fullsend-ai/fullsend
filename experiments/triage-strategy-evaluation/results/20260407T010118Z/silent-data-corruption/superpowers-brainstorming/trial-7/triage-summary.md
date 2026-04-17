# Triage Summary

**Title:** Nightly Salesforce sync truncates international phone country codes longer than 2 digits

## Problem
The nightly batch import from Salesforce CRM into TaskFlow is truncating country codes on international phone numbers to 2 digits. Numbers with 3-digit country codes (e.g., Ireland +353 → +35) are corrupted, while numbers with 1-2 digit country codes (US +1, UK +44) are unaffected. This has been happening gradually since roughly two weeks ago, with a few more records corrupted each nightly sync. An estimated 50-100 of ~2,000 customer records are affected so far.

## Root Cause Hypothesis
A change approximately two weeks ago in the phone number import/parsing logic — likely in country code extraction or a database column constraint — is truncating country codes to a maximum of 2 characters. This could be a regex pattern that assumes country codes are 1-2 digits, a VARCHAR(2) column or field length limit, or a parsing library update that changed behavior.

## Reproduction Steps
  1. Identify a Salesforce customer record with a 3+ digit country code (e.g., Ireland +353 xxx xxxx)
  2. Run the nightly sync process (or trigger it manually for that record)
  3. Check the resulting phone number in TaskFlow — the country code should be truncated to 2 digits (+35)

## Environment
TaskFlow with nightly batch import from Salesforce CRM. Issue began approximately 2 weeks ago, suggesting a recent code or configuration change.

## Severity: high

## Impact
Support team cannot reach international customers with 3+ digit country codes. ~50-100 records affected and growing daily with each sync. Data integrity issue that erodes trust in the customer database.

## Recommended Fix
1. Check git history for changes ~2 weeks ago in the Salesforce sync / phone number import code path. 2. Look for country code field length constraints (DB column, validation regex, parsing logic) that limit to 2 characters. 3. Fix the constraint to allow 3-digit country codes (max is 3 digits per E.164). 4. Re-sync affected records from Salesforce to restore correct numbers.

## Proposed Test Case
Import phone numbers with 1-, 2-, and 3-digit country codes (+1, +44, +353, +598) through the sync pipeline and verify all are stored and displayed correctly with full country codes intact.

## Information Gaps
- Exact mechanism of the Salesforce sync (API, CSV, middleware) — would help locate code but not change the fix
- Precise change that occurred two weeks ago (reporter is unsure)
- Whether any 3-digit country code numbers are unaffected (would clarify if truncation is universal or conditional)
