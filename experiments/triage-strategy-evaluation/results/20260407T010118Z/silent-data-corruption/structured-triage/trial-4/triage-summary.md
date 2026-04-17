# Triage Summary

**Title:** International phone numbers with 3+ digit country codes are truncated during Salesforce batch import

## Problem
The nightly Salesforce-to-TaskFlow batch import is truncating international phone numbers for countries with longer country codes. For example, Ireland's +353 becomes +35 and Antigua's +1268 is similarly cut. US (+1) and UK (+44) numbers are unaffected. Approximately 50-100 of ~2,000 customer records are impacted.

## Root Cause Hypothesis
A recent change (likely in TaskFlow v2.3.1, possibly the 'phone number formatting improvements' the reporter vaguely recalls from the changelog) introduced a bug in phone number parsing or normalization during import. The code likely assumes a maximum country code length of 2 digits, or applies a fixed-width truncation to the leading portion of the number. This would explain why +1 (US) and +44 (UK) are fine but +353 (3 digits) and +1268 (4 digits as a sub-code) are corrupted.

## Reproduction Steps
  1. Set up a Salesforce integration with TaskFlow v2.3.1 containing customer records with international phone numbers (3+ digit country codes, e.g., +353 for Ireland, +1268 for Antigua)
  2. Run the nightly batch import (or trigger it manually)
  3. Compare the imported phone numbers in TaskFlow against the Salesforce originals
  4. Observe that country codes longer than 2 digits are truncated

## Environment
TaskFlow v2.3.1, Salesforce CRM integration via nightly batch import, standard field mappings (unchanged for months)

## Severity: high

## Impact
~50-100 customer records have incorrect phone numbers, causing the support team to reach wrong numbers when contacting international customers. This is a data integrity issue that erodes trust in the system and directly impacts customer communication.

## Recommended Fix
1. Check the v2.3.1 changelog for any phone number formatting or normalization changes. 2. Inspect the Salesforce import pipeline's phone number parsing/normalization code — look for assumptions about country code length (e.g., fixed 2-digit extraction or truncation). 3. Fix the parsing to handle variable-length country codes per the E.164 standard. 4. Provide a one-time re-import or correction script to fix the ~50-100 affected records from the clean Salesforce data.

## Proposed Test Case
Unit test the phone number normalization function with international numbers of varying country code lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4-digit NANP sub-code). Assert that all are stored without truncation. Additionally, integration-test a batch import with a mix of these numbers and verify round-trip fidelity.

## Information Gaps
- Exact TaskFlow version or commit that introduced the regression (reporter unsure if the changelog note about 'phone number formatting improvements' aligns with the timeline)
- Import job logs that might show warnings or errors during phone number processing
- Whether previously-correct records were retroactively corrupted by re-import or only newly created records are affected
