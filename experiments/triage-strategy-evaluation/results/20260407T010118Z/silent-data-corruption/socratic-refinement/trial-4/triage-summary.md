# Triage Summary

**Title:** Nightly Salesforce import truncates international phone country codes longer than 2 digits

## Problem
Customer phone numbers imported from Salesforce via the nightly batch sync are being corrupted for international numbers with country codes longer than 2 digits. Approximately 50-100 of ~2,000 records are affected. US (+1) and UK (+44) numbers are unaffected. The issue started approximately two weeks ago and coincides with a TaskFlow phone number formatting update.

## Root Cause Hypothesis
A recent change to TaskFlow's phone number formatting/parsing logic (noted in the changelog ~2 weeks ago) is truncating country codes to a maximum of 2 digits during the import process. For example, Ireland's +353 becomes +35, and +1268 (Antigua) becomes +12. Numbers with 1-2 digit country codes (US, UK) pass through correctly.

## Reproduction Steps
  1. Set up a Salesforce record with an international phone number using a 3+ digit country code (e.g., +353 1 234 5678 for Ireland)
  2. Run the nightly batch import (or trigger it manually)
  3. Compare the resulting phone number in TaskFlow to the Salesforce source
  4. Observe that the country code is truncated to 2 digits

## Environment
TaskFlow with nightly Salesforce CRM batch import; issue began ~2 weeks ago after a phone number formatting update

## Severity: high

## Impact
Support team cannot reach ~50-100 international customers. Ongoing nightly sync continues to overwrite numbers with corrupted versions, compounding the issue. Data can be restored from Salesforce once the bug is fixed.

## Recommended Fix
1. Identify the changelog entry from ~2 weeks ago related to phone number formatting. 2. Review the phone number parsing/normalization code in the Salesforce import pipeline — look for hardcoded country code length assumptions (likely capping at 2 digits). 3. Fix the parser to handle variable-length country codes per the ITU E.164 standard (1-3 digits). 4. Consider pausing the nightly sync immediately to prevent further corruption. 5. After fixing, re-import affected records from Salesforce to restore correct numbers.

## Proposed Test Case
Import phone numbers with country codes of varying lengths (1 digit: +1, 2 digits: +44, 3 digits: +353, +1268 as part of NANP) and verify all are stored correctly without truncation in TaskFlow.

## Information Gaps
- Exact changelog entry and commit for the phone number formatting change
- Whether the truncation happens during parsing, storage, or display
- Whether any other phone number fields (e.g., fax, mobile) are also affected
