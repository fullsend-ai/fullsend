# Triage Summary

**Title:** Phone number country codes truncated during Salesforce import for codes longer than 2 digits

## Problem
International phone numbers imported from Salesforce via the nightly sync are having their country codes truncated. Numbers with country codes of 3 or more digits (e.g., Ireland +353, Antigua +1268) are being cut short, while 1-2 digit country codes (US +1, UK +44) are unaffected. Approximately 50-100 of 2,000 customer records are affected. The data is correct in Salesforce, so the corruption occurs during TaskFlow's import processing.

## Root Cause Hypothesis
A recent change to the phone number normalizer (approximately 2 weeks ago, intended to improve international format support) likely introduced a bug that truncates country codes to a maximum of 2 digits. This could be a fixed-length substring, an incorrect parsing regex, or a faulty country-code lookup table that silently falls back to a prefix match.

## Reproduction Steps
  1. Set up a Salesforce export containing phone numbers with country codes of varying lengths: +1 (US), +44 (UK), +353 (Ireland), +1268 (Antigua)
  2. Run the nightly import process (or trigger it manually) on TaskFlow v2.3.1
  3. Compare the imported phone numbers against the Salesforce source
  4. Observe that +1 and +44 numbers are correct, but +353 is stored as +35 and +1268 is stored as +12 or similar truncation

## Environment
TaskFlow v2.3.1, nightly import from Salesforce, international phone numbers in E.164 or similar format

## Severity: high

## Impact
Support team cannot reach affected international customers. ~50-100 customer records have corrupted phone numbers. Silent data corruption — no errors are raised, so the issue compounds with each nightly import. Existing corrupted records will need a backfill from Salesforce.

## Recommended Fix
Investigate the phone number normalizer change made approximately 2 weeks ago. Look for logic that parses or validates country codes — likely a fixed-width truncation or an incomplete country code table. The fix should correctly handle variable-length country codes (1-3 digits per ITU E.164). After fixing, run a one-time backfill of affected records from Salesforce to restore the correct numbers.

## Proposed Test Case
Unit test the phone number normalizer with country codes of length 1 (+1), 2 (+44), and 3 (+353), as well as numbers where the country code plus area code could be ambiguous (+1268). Assert that the full country code is preserved in each case and the remaining digits are intact.

## Information Gaps
- Exact commit or PR that changed the phone number normalizer
- Whether the import process logs any warnings or debug output for these records
- Whether numbers corrupted in earlier imports are being re-corrupted on each subsequent run or only new/changed records are affected
