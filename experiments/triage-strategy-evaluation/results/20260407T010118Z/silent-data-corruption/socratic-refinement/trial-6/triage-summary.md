# Triage Summary

**Title:** Nightly Salesforce sync truncates phone country codes longer than 2 digits

## Problem
Customer phone numbers imported from Salesforce via the nightly sync are being corrupted. Country codes with 3 or more digits are truncated to 2 digits (e.g., Ireland's +353 becomes +35, Caribbean +1268 becomes +12). Numbers with 1-2 digit country codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of ~2,000 records are impacted.

## Root Cause Hypothesis
A recent change (~2 weeks ago) to phone number formatting or international number handling introduced a bug that parses or stores country codes with a fixed 2-digit width, truncating any country code longer than 2 digits.

## Reproduction Steps
  1. Find a customer record in Salesforce with a phone number whose country code is 3+ digits (e.g., +353 for Ireland, +1268 for Antigua)
  2. Wait for (or manually trigger) the nightly Salesforce-to-TaskFlow sync
  3. Compare the phone number in TaskFlow to the Salesforce source — the country code should be truncated to 2 digits

## Environment
TaskFlow with Salesforce CRM integration via nightly automated sync

## Severity: high

## Impact
Support team cannot reach customers with 3+ digit country codes. Affects ~50-100 records currently, and will corrupt additional records on every future sync run. Outbound calls to affected customers reach wrong numbers.

## Recommended Fix
Check the git log / changelog for changes made approximately 2 weeks ago related to phone number formatting or international number handling. Look for any parsing logic that extracts or formats country codes — likely a substring, regex, or field-width issue that caps country codes at 2 digits. Fix the parsing to handle variable-length country codes (1-3 digits per E.164). After fixing, re-sync affected records from Salesforce to restore correct numbers.

## Proposed Test Case
Unit test: given phone numbers with country codes of varying lengths (+1, +44, +353, +1268, +86), verify that the sync/formatting pipeline preserves the full number unchanged. Include edge cases for country codes of 1, 2, and 3 digits, and for NANP numbers like +1268 where the full dialing prefix is 4 digits.

## Information Gaps
- Exact date and content of the code change that introduced the bug (dev team can check their own changelog)
- Whether already-corrupted records will be automatically corrected by a re-sync or need a manual data fix
- Whether any other fields in the sync are similarly affected by the formatting change
