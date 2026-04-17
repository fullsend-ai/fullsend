# Triage Summary

**Title:** Nightly Salesforce sync truncates international phone country codes to 2 digits

## Problem
Approximately 50-100 of ~2,000 customer phone numbers have been corrupted by the nightly Salesforce-to-TaskFlow sync. Country codes longer than 2 digits are being truncated to exactly 2 digits, mangling international phone numbers while leaving short-code countries (US +1, UK +44) unaffected.

## Root Cause Hypothesis
A recent update (~2 weeks ago) to the phone number handling or international format support in the Salesforce sync pipeline introduced a bug that truncates the country code portion of phone numbers to a maximum of 2 digits. This likely occurs in a parsing or normalization step that incorrectly assumes all country codes are 1-2 digits.

## Reproduction Steps
  1. Create or identify a customer record in Salesforce with a 3+ digit country code (e.g., Ireland +353 21 234 5678)
  2. Wait for the nightly sync to run (or trigger it manually)
  3. Check the phone number in TaskFlow — the country code should be truncated to 2 digits (e.g., +35 21 234 5678)
  4. Verify a US (+1) number syncs correctly as a control case

## Environment
Nightly Salesforce CRM → TaskFlow sync pipeline. Issue started approximately 2 weeks ago, coinciding with a reported update to phone number / international format handling.

## Severity: high

## Impact
Support team cannot reach ~50-100 international customers by phone. Data integrity of customer contact records is compromised. Affected customers are exclusively those in countries with 3+ digit country codes (e.g., Ireland, India, Caribbean nations). Each nightly sync may be re-corrupting any manually corrected records.

## Recommended Fix
1. Check the Salesforce sync code's phone number parsing/normalization logic for a recent change (~2 weeks ago) that handles country codes — look for a substring, regex, or field-length constraint that limits the country code to 2 characters. 2. Fix the parser to support country codes of 1-3 digits per the ITU E.164 standard. 3. Run a one-time remediation: re-sync all phone numbers from Salesforce (the source of truth) to TaskFlow to restore corrupted records. 4. Consider adding a validation check that flags phone numbers whose country code doesn't match a known valid code.

## Proposed Test Case
Unit test the phone number normalization function with inputs covering 1-digit (+1 US), 2-digit (+44 UK), 3-digit (+353 Ireland), and combined country+area codes that look long (+1268 Antigua). Assert that the full country code is preserved in all cases and the resulting number matches E.164 format.

## Information Gaps
- Exact changelog entry or commit that introduced the phone number handling change ~2 weeks ago (discoverable from version control)
- Whether manually corrected records are being re-corrupted on each nightly sync cycle
- Whether phone numbers entered directly in TaskFlow (not via sync) are also affected by the same normalization bug
