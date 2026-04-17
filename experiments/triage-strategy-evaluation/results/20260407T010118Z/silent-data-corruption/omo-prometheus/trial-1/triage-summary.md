# Triage Summary

**Title:** Nightly Salesforce import truncates phone number country codes to 2 digits

## Problem
Customer phone numbers imported from Salesforce via the nightly batch sync are having their country codes truncated to exactly 2 digits. Numbers with 1- or 2-digit country codes (US +1, UK +44, India +91) are unaffected, but 3+ digit country codes are clipped (Ireland +353 → +35, Antigua +1268 → +12). Approximately 50-100 of ~2,000 customer records are affected. The corruption is in the stored data — support staff get wrong numbers when calling affected customers.

## Root Cause Hypothesis
A recent change (likely in TaskFlow v2.3.1, deployed ~2 weeks ago) introduced a 2-character limit on the country code portion of phone numbers during the Salesforce import parsing/storage path. This could be a schema migration that shortened a column, a new validation rule, a regex change in the phone number parser, or a field-length constraint added to the country code component of a structured phone number type.

## Reproduction Steps
  1. Set up a Salesforce sync with customer records containing international phone numbers with 3+ digit country codes (e.g., Ireland +353, Antigua +1268)
  2. Run the nightly batch import (or trigger it manually)
  3. Compare the phone numbers stored in TaskFlow against the Salesforce source
  4. Observe that country codes are truncated to 2 digits while the remaining digits of the number are preserved

## Environment
TaskFlow (believed to be v2.3.1), nightly batch import from Salesforce CRM. No changes were made to the import configuration or Salesforce field mappings. Issue began approximately 2 weeks ago.

## Severity: high

## Impact
~50-100 customer records have corrupted phone numbers. Support team cannot reach affected international customers. All customers with country codes of 3+ digits are affected on every import cycle, so the problem is ongoing and may be re-corrupting any manual fixes.

## Recommended Fix
1. Check the v2.3.1 changelog and diff for any changes to phone number parsing, storage schema, or import field mappings. 2. Look for a country code field/column with a VARCHAR(2) or equivalent constraint, or a parsing regex that captures only 2 digits for the country code. 3. Fix the constraint to allow up to 4 digits for country codes (the ITU maximum is 3, but +1xxx Caribbean codes are effectively 4). 4. Re-import affected records from Salesforce to restore correct data. 5. Consider adding a validation check to the import pipeline that flags phone numbers that changed length between source and destination.

## Proposed Test Case
Import a batch of phone numbers covering all country code lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4-digit Caribbean). Verify that all numbers are stored exactly as provided, with no truncation. Regression test should assert character-level equality between source and stored values for the country code portion.

## Information Gaps
- Exact TaskFlow version — reporter believes v2.3.1 but is not certain of timing
- Whether already-corrupted records get re-corrupted on each import cycle (likely yes, since Salesforce source is correct and import runs nightly)
- Whether the truncation happens at parse time, storage time, or display time (calling failures confirm it is at least stored incorrectly)
