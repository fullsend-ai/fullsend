# Triage Summary

**Title:** Salesforce sync truncates phone country codes longer than 2 digits

## Problem
The nightly Salesforce-to-TaskFlow sync is corrupting phone numbers for countries whose dialing codes are longer than 2 digits. Country codes are being truncated to 2 digits (e.g., +1268 becomes +12, +353 becomes +35), while countries with 1-2 digit codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of ~2,000 customer records are affected.

## Root Cause Hypothesis
A recent update to the phone number formatting/parsing logic (likely ~2 weeks ago, possibly referenced in the changelog as international number formatting support) introduced a bug that truncates the country code portion of phone numbers to a maximum of 2 digits. The parsing code likely assumes all country codes are 1-2 digits, discarding the remaining digits or misinterpreting where the country code ends and the subscriber number begins.

## Reproduction Steps
  1. Ensure a Salesforce contact record has a phone number with a 3+ digit country code (e.g., +1268-555-1234 for Antigua or +353-1-555-1234 for Ireland)
  2. Run or wait for the nightly Salesforce sync to TaskFlow
  3. Check the phone number stored in TaskFlow — the country code should be truncated to 2 digits

## Environment
Salesforce-to-TaskFlow nightly sync integration; issue affects international phone numbers with 3+ digit country codes

## Severity: high

## Impact
Support team cannot reach ~50-100 international customers by phone. Data integrity issue that silently corrupts contact records on every sync cycle, meaning manual corrections are overwritten nightly.

## Recommended Fix
1. Check the changelog/commits from ~2 weeks ago for changes to phone number parsing or international formatting. 2. Review the country code parsing logic in the Salesforce sync pipeline — look for hardcoded length limits or incorrect assumptions about country code length (ITU E.164 country codes range from 1-3 digits). 3. Fix the parser to correctly handle variable-length country codes. 4. Run a one-time re-sync or repair of affected records after the fix is deployed.

## Proposed Test Case
Test the sync with phone numbers using 1-digit (+1), 2-digit (+44), and 3-digit (+353, +1268) country codes and verify all are stored correctly in TaskFlow without truncation. Include edge cases like +1 (NANP) vs +1268 (Antigua, which shares the +1 prefix but has a longer code).

## Information Gaps
- Exact version or commit that introduced the phone formatting change (~2 weeks ago)
- Whether the sync pipeline has intermediate transformation steps between Salesforce and TaskFlow beyond the formatter
