# Triage Summary

**Title:** Salesforce import truncates international phone country codes to 2 digits

## Problem
The nightly Salesforce CRM batch import is corrupting international phone numbers by truncating their country codes to 2 digits. An estimated 50-100 out of ~2,000 customer records are affected, causing support staff to reach wrong numbers.

## Root Cause Hypothesis
The phone number parsing or storage logic in the Salesforce import pipeline is applying a fixed 2-digit limit to the country code portion of international numbers. Numbers with 1- or 2-digit country codes (US +1, UK +44) pass through correctly, while numbers with 3- or 4-digit country codes (Ireland +353, Antigua +1268) are truncated to 2 digits.

## Reproduction Steps
  1. Set up or locate the nightly Salesforce CRM import integration
  2. Ensure the Salesforce source contains a contact with an international phone number whose country code is longer than 2 digits (e.g., +353 1 234 5678 for Ireland)
  3. Run the import (or wait for the nightly sync)
  4. Check the imported record in TaskFlow — the country code will be truncated to 2 digits (e.g., +35 1 234 5678)

## Environment
TaskFlow with nightly batch import from Salesforce CRM (automated sync). Issue observed for at least ~1 week. Affects international numbers with 3+ digit country codes.

## Severity: high

## Impact
50-100 customer contact records have corrupted phone numbers, preventing support staff from reaching those customers. Data corruption is ongoing with each nightly import, so affected records are being re-corrupted even if manually corrected. Primarily impacts customers in countries with 3- or 4-digit calling codes.

## Recommended Fix
Investigate the phone number parsing logic in the Salesforce import pipeline. Look for a hard-coded 2-character substring or regex group capturing the country code. The fix should preserve the full country code as provided by Salesforce (likely stored in E.164 format). After fixing the parser, run a one-time re-import or correction pass to repair the ~50-100 affected records.

## Proposed Test Case
Import a batch of phone numbers including: +1 (US, 1-digit code), +44 (UK, 2-digit), +353 (Ireland, 3-digit), +1268 (Antigua, 4-digit). Verify all are stored exactly as provided in the source data, with no truncation of the country code.

## Information Gaps
- Exact TaskFlow version and import pipeline configuration
- Precise date the truncation started (could correlate with a deployment or config change)
- Whether import logs show any warnings or errors during phone number processing
- Whether the issue also affects phone number fields other than the primary contact number
