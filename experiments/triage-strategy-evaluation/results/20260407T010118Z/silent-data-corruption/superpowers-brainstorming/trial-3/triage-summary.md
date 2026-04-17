# Triage Summary

**Title:** Salesforce nightly import truncates phone country codes longer than 2 digits

## Problem
Customer phone numbers imported from Salesforce via the nightly batch sync are having their country codes truncated to exactly 2 digits. This corrupts phone numbers for any country whose calling code is 3 or 4 digits (e.g., Ireland +353, Antigua +1268), while countries with 1- or 2-digit codes (US +1, UK +44) are unaffected. An estimated 50–100 of ~2,000 customer records are affected.

## Root Cause Hypothesis
The import code (or the database column storing the country code portion) is limited to 2 characters. This could be a VARCHAR(2) column, a substring/slice operation that assumes country codes are at most 2 digits, or a regex pattern that captures only 2 digits after the '+' sign.

## Reproduction Steps
  1. Ensure a Salesforce contact record exists with a phone number whose country code is 3+ digits (e.g., +353 1 234 5678 for Ireland)
  2. Run the nightly Salesforce-to-TaskFlow batch import (or trigger it manually)
  3. Compare the phone number stored in TaskFlow against the Salesforce source
  4. Observe that the country code is truncated to 2 digits (e.g., +35 1 234 5678)

## Environment
Production — nightly batch import from Salesforce CRM to TaskFlow

## Severity: high

## Impact
Support team cannot reach affected customers by phone. Approximately 50–100 records are corrupted, likely all customers with 3- or 4-digit country codes (countries outside North America and a handful of 2-digit-code nations). Data integrity issue that worsens with each nightly sync if not corrected.

## Recommended Fix
Inspect the Salesforce import code path for phone number parsing. Look for: (1) a database column constraint limiting the country code portion to 2 characters, (2) a substring or slice operation like `country_code[:2]` or equivalent, (3) a regex that captures only 2 digits after '+'. Country codes range from 1 to 4 digits — the fix should accommodate the full range. After fixing the import logic, run a one-time backfill by re-importing phone numbers from Salesforce for affected records.

## Proposed Test Case
Import phone numbers with country codes of varying lengths — 1 digit (+1 US), 2 digits (+44 UK), 3 digits (+353 Ireland), and 4 digits (+1268 Antigua) — and verify all are stored and displayed correctly in TaskFlow without truncation.

## Information Gaps
- Exact import mechanism (API call, file export/import, middleware) — discoverable from codebase
- Whether this is a recent regression or has always been broken for 3+ digit country codes
- Whether the phone number is stored as a single field or split into country code and local number parts — discoverable from schema
