# Triage Summary

**Title:** Phone numbers with 3+ digit country codes are truncated to 2 digits during Salesforce sync

## Problem
Customer phone numbers imported via the nightly Salesforce batch sync are having their country codes truncated to exactly two digits. Numbers with 1- or 2-digit country codes (US +1, UK +44) are unaffected, but longer codes like Ireland (+353 → +35) and Antigua (+1268 → +12) are corrupted. An estimated 50-100 of ~2,000 customer records are affected.

## Root Cause Hypothesis
The Salesforce sync pipeline (or a phone number parsing/formatting layer it passes through) is truncating the country code field to a maximum of 2 characters. This is likely a regression introduced approximately two weeks ago — possibly related to a phone number formatting change mentioned by the reporter. A field length constraint, substring operation, or regex extracting the country code may be using a hard-coded width of 2.

## Reproduction Steps
  1. Ensure a Salesforce record exists with a phone number that has a 3+ digit country code (e.g., +353 1 234 5678 for Ireland)
  2. Run the nightly Salesforce batch import (or trigger it manually)
  3. Check the resulting TaskFlow customer record
  4. Observe the country code is truncated to 2 digits (e.g., +35 1 234 5678)

## Environment
TaskFlow v2.3.1, self-hosted, with nightly batch sync from Salesforce CRM

## Severity: high

## Impact
50-100 customer records have corrupted phone numbers, preventing the support team from reaching customers in countries with 3+ digit country codes. Data integrity issue affecting customer communications. The corruption is ongoing with each nightly sync, potentially re-corrupting any manual fixes.

## Recommended Fix
Investigate the Salesforce sync code path for phone number parsing — look for country code extraction logic that assumes a max 2-digit code. Check git history for changes to phone number formatting or import logic from approximately two weeks ago (around TaskFlow v2.3.x timeframe). The ITU E.164 standard allows country codes of 1-3 digits; ensure the parser handles all valid lengths. After fixing, a data remediation script should re-sync affected records from Salesforce.

## Proposed Test Case
Create test phone numbers with 1-, 2-, and 3-digit country codes (+1, +44, +353, +1268) and run them through the sync/import pipeline. Assert that all country codes are preserved in full. Include edge cases like +1268 (which starts with +1 but is actually a 4-digit code for Antigua).

## Information Gaps
- Exact date of the deployment or patch applied ~2 weeks ago that may have introduced the regression
- Sync job logs that might show parsing warnings or errors
- Whether manually-entered phone numbers (not from the sync) are also affected by the same truncation
