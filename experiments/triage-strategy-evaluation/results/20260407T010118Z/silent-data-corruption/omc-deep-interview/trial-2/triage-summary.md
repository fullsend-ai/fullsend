# Triage Summary

**Title:** Phone numbers with 3+ digit country codes truncated to 2 digits during Salesforce nightly sync

## Problem
Approximately 50-100 of ~2,000 customer phone number records in TaskFlow have corrupted country codes. Numbers with country codes longer than 2 digits (e.g., Ireland +353, Antigua +1268) are being truncated to 2 digits (+35, +12), while 1-2 digit country codes (US +1, UK +44) are unaffected. The corruption originated in the nightly Salesforce-to-TaskFlow sync and the source data in Salesforce remains correct.

## Root Cause Hypothesis
A code change made approximately 2 weeks ago to phone number formatting or normalization logic in the Salesforce sync pipeline is truncating the country code portion of international phone numbers to a maximum of 2 digits. This is likely a regex or substring operation that assumes all country codes are 1-2 digits, or a field-width constraint applied during parsing.

## Reproduction Steps
  1. Identify a customer record in Salesforce with a country code of 3+ digits (e.g., +353 for Ireland)
  2. Confirm the phone number is correct and complete in Salesforce
  3. Wait for or manually trigger the nightly Salesforce-to-TaskFlow sync
  4. Check the same customer record in TaskFlow — the country code should appear truncated to 2 digits

## Environment
TaskFlow application with nightly API-based data sync from Salesforce CRM. Approximately 2,000 customer records. Issue began approximately 2 weeks ago, coinciding with a developer change to phone number formatting/normalization logic.

## Severity: high

## Impact
Support team cannot reach affected customers by phone. Data corruption is ongoing — the nightly sync likely re-corrupts any manually corrected numbers. Affects international customers with 3+ digit country codes (estimated 50-100 records).

## Recommended Fix
1. Review git history from ~2 weeks ago for changes to phone number formatting, normalization, or parsing in the Salesforce sync code. Look for regex patterns, substring operations, or field-width constraints on the country code portion. 2. Fix the formatting logic to preserve full country codes of any length (1-4 digits per E.164). 3. After deploying the fix, re-run the sync (or a targeted backfill) to restore correct numbers from Salesforce. 4. Consider adding a validation check that flags phone numbers whose country code doesn't match a known valid code.

## Proposed Test Case
Create test records with phone numbers using country codes of varying lengths (1 digit: +1, 2 digits: +44, 3 digits: +353, 4 digits: +1268) and run them through the sync/normalization pipeline. Assert that all country codes are preserved in full after processing.

## Information Gaps
- Exact date and nature of the phone number formatting code change ~2 weeks ago
- Whether manually correcting a number in TaskFlow survives the next nightly sync (reporter testing this)
- Sync logs from the period when corruption began
- Whether the issue affects only the sync path or also manual phone number entry in TaskFlow
