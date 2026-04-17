# Triage Summary

**Title:** Nightly CRM sync truncates country codes longer than 2 digits in phone numbers

## Problem
The nightly Salesforce-to-TaskFlow sync is writing truncated phone numbers into the database. Country codes longer than two characters are being clipped to exactly two characters (e.g., +353 becomes +35, +1268 becomes +12). Numbers with 1- or 2-digit country codes (+1, +44) are unaffected. Approximately 50–100 of ~2,000 customer records are corrupted. The data is genuinely wrong in the database, not a display issue.

## Root Cause Hypothesis
A change made approximately two weeks ago to the phone number formatting or normalization logic in the CRM sync script is truncating the country code portion of phone numbers to a maximum of two characters. This is likely a hard-coded length limit, an incorrect substring/slice operation, or a regex pattern that assumes all country codes are 1–2 digits.

## Reproduction Steps
  1. Identify the recent change to the sync script's phone number formatting/normalization (check git history for commits ~2 weeks ago)
  2. Run the sync logic against a test record with a phone number that has a 3+ digit country code (e.g., +353 1 234 5678 for Ireland)
  3. Observe that the resulting phone number is truncated to +35 1 234 5678
  4. Repeat with a 1-digit country code (e.g., +1 555 123 4567) and confirm it is unaffected

## Environment
TaskFlow production database, nightly sync process pulling from Salesforce CRM. Issue introduced approximately two weeks ago with a code change to the sync script's phone number handling.

## Severity: high

## Impact
Support team cannot reach affected customers by phone. ~50–100 records currently corrupted and the number grows with each nightly sync as records are re-processed. Data loss is real (overwritten, not reversible without re-import from Salesforce).

## Recommended Fix
1. Review git history for the sync script's phone number normalization code changed ~2 weeks ago. 2. Fix the truncation logic to support country codes of 1–4 digits. 3. After deploying the fix, re-run the sync (or a targeted restore) to pull correct phone numbers from Salesforce for all affected records.

## Proposed Test Case
Unit test the phone number normalization function with country codes of varying lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4 digits). Assert that the full country code and subscriber number are preserved in each case.

## Information Gaps
- Exact file and commit that introduced the regression (discoverable via git history)
- Whether the truncation happens in application code or at the database schema level (e.g., a column width change)
- Full scope of affected country codes beyond Ireland (+353) and Antigua (+1268)
