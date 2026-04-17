# Triage Summary

**Title:** Country codes longer than 2 digits truncated during Salesforce phone number sync (v2.3.1 regression)

## Problem
Customer phone numbers imported via the nightly Salesforce sync are having their country codes truncated to exactly 2 digits. Numbers with 1- or 2-digit country codes (e.g., US +1, UK +44) are unaffected, but numbers with 3+ digit country codes (e.g., Ireland +353, Caribbean +1268) are silently corrupted. Approximately 50-100 of ~2,000 customer records are affected, causing support team calls to reach wrong numbers.

## Root Cause Hypothesis
A phone number formatting change introduced in or around TaskFlow v2.3.1 is truncating the country code portion of international phone numbers to a maximum of 2 characters. This likely involves a substring operation, fixed-width field, or regex pattern in the sync/import pipeline that assumes country codes are at most 2 digits long.

## Reproduction Steps
  1. Set up a Salesforce record with a phone number that has a 3+ digit country code, e.g., +353 1 234 5678 (Ireland)
  2. Run the nightly Salesforce-to-TaskFlow sync (or trigger it manually)
  3. Inspect the resulting phone number in TaskFlow
  4. Observe that the country code is truncated to 2 digits: +35 1 234 5678

## Environment
TaskFlow v2.3.1, Salesforce CRM integration via nightly sync

## Severity: high

## Impact
Support team cannot reach customers with 3+ digit country codes by phone. Data integrity issue affecting ~50-100 records. Ongoing — each nightly sync re-corrupts any manually corrected numbers.

## Recommended Fix
Examine the phone number formatting/parsing code changed in v2.3.1 (check the changelog entry about phone number formatting). Look for any operation that limits country code length to 2 characters — likely a substring(0,2), a varchar(2) column, or a regex capture group like `\+(\d{1,2})`. Fix should accommodate country codes up to 4 digits per the ITU E.164 standard. After fixing, re-sync affected records from Salesforce to restore correct numbers.

## Proposed Test Case
Unit test the phone number parser/formatter with country codes of varying lengths: +1 (US, 1 digit), +44 (UK, 2 digits), +353 (Ireland, 3 digits), +1268 (Antigua, 4 digits). Assert that the full country code is preserved in each case. Additionally, add an integration test that round-trips a +353 number through the Salesforce sync pipeline and verifies it arrives intact.

## Information Gaps
- Exact changelog entry and code diff from v2.3.1 related to phone formatting (developer can find this from version control)
- Whether any other fields beyond phone numbers were affected by the same formatting change
