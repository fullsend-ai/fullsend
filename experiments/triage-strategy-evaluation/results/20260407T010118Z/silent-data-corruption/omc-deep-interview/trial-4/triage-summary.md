# Triage Summary

**Title:** Salesforce import truncates international phone country codes longer than 2 digits

## Problem
The nightly Salesforce-to-TaskFlow customer sync is truncating phone number country codes that are longer than two digits. For example, Ireland's +353 becomes +35, and Antigua's +1268 becomes +12. US (+1) and UK (+44) numbers are unaffected because their country codes are 1-2 digits. Approximately 50-100 of ~2,000 customer records are affected.

## Root Cause Hypothesis
The phone number import/parsing logic is truncating or validating country codes against a maximum length of 2 digits. This is likely a regression introduced in a TaskFlow update approximately two weeks ago — possibly a new phone number normalization step, a schema change limiting the country code field width, or a regex/parsing change in the import pipeline.

## Reproduction Steps
  1. Set up a Salesforce sync with test customer records containing phone numbers with 1-digit (+1), 2-digit (+44), and 3+ digit (+353, +1268) country codes
  2. Run the nightly import process (or trigger it manually)
  3. Compare the imported phone numbers in TaskFlow against the Salesforce source records
  4. Observe that country codes with 3+ digits are truncated to 2 digits

## Environment
TaskFlow with Salesforce CRM integration via nightly batch import. Issue began approximately two weeks ago (late March 2026). Salesforce-side data is confirmed correct.

## Severity: high

## Impact
Support team cannot reach ~50-100 international customers by phone using TaskFlow contact data. Currently working around the issue by looking up numbers directly in Salesforce, which slows down operations. Data corruption recurs nightly since the import overwrites any manual corrections.

## Recommended Fix
1. Review git history of the phone number import/parsing code for changes made ~2 weeks ago. 2. Check for country code validation, regex patterns, or database column constraints that limit country code length to 2 characters. 3. Fix the parsing logic to support variable-length country codes (1-3 digits per E.164). 4. Run a one-time re-import or corrective sync to fix the ~50-100 affected records.

## Proposed Test Case
Unit test the phone number import parser with E.164 numbers spanning all country code lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4 digits including trunk prefix). Assert that the full country code and subscriber number are preserved exactly as provided in the source data.

## Information Gaps
- Exact TaskFlow version or commit that introduced the regression (~2 weeks ago)
- Whether the phone number field schema was changed (e.g., column width, type constraint)
- Whether any Salesforce-side format changes coincided with the issue
