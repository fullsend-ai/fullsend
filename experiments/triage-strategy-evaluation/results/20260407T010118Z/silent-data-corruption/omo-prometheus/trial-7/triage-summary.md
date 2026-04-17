# Triage Summary

**Title:** Phone number formatting update truncates country codes longer than 2 digits during Salesforce sync import

## Problem
Customer phone numbers with country codes longer than 2 digits (e.g., +353 Ireland, +1268 Antigua) are being truncated to 2 digits after the '+' during the nightly Salesforce CRM batch import. Numbers with 1-2 digit country codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of ~2,000 records are corrupted. Source data in Salesforce remains correct.

## Root Cause Hypothesis
A recent TaskFlow update (approximately 2 weeks ago) that 'improved phone number formatting' introduced a bug that truncates the country code portion of international phone numbers to a maximum of 2 digits. The formatting/normalization logic likely parses the country code with a fixed 2-character assumption or an incorrect substring operation, discarding additional digits before passing the rest of the number through.

## Reproduction Steps
  1. Ensure a Salesforce-synced customer record has a phone number with a country code longer than 2 digits (e.g., +353 1 234 5678 for Ireland)
  2. Run the nightly batch import sync (or trigger it manually)
  3. Observe that the phone number in TaskFlow is truncated (e.g., +35 1 234 5678)
  4. Verify that numbers with 1-2 digit country codes (e.g., +1, +44) are imported correctly

## Environment
TaskFlow with nightly Salesforce CRM batch import integration. Issue began approximately 2 weeks ago, likely coinciding with a TaskFlow update related to phone number formatting.

## Severity: high

## Impact
~50-100 customer records have corrupted phone numbers, preventing support team from reaching customers. Data corruption is ongoing — each nightly sync re-applies the truncation. Affected customers are those with country codes longer than 2 digits (many international numbers including Ireland +353, Antigua +1268, and likely others).

## Recommended Fix
1. Identify the recent phone number formatting change in the codebase (likely ~2 weeks old). 2. Review the country code parsing/normalization logic — look for hardcoded length limits, substring(0,2) operations, or incorrect ITU country code lookups. 3. Fix the parser to handle variable-length country codes (1-3 digits per E.164 standard). 4. Re-run the Salesforce sync to restore correct numbers from the intact CRM data. 5. Advise the reporter to pause the nightly sync until the fix is deployed to prevent further corruption.

## Proposed Test Case
Test phone number formatting with country codes of varying lengths: 1 digit (+1 US), 2 digits (+44 UK), 3 digits (+353 Ireland, +1268 Antigua, +855 Cambodia). Verify all are stored and displayed with the full, correct country code after import processing.

## Information Gaps
- Exact TaskFlow version or release that introduced the phone number formatting change
- Whether the truncation happens at import/parsing time or at display/rendering time (likely import, since stored values are wrong, but worth confirming)
- Whether any data write-back from TaskFlow to Salesforce could eventually corrupt the source data
