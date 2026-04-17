# Triage Summary

**Title:** Salesforce sync truncates international phone numbers with 3+ digit country codes

## Problem
The nightly Salesforce-to-TaskFlow sync is corrupting international phone numbers by truncating country codes that are 3 or more digits long. For example, Ireland's +353 becomes +35. US (+1) and UK (+44) numbers are unaffected. Approximately 50-100 of ~2,000 customer records are affected, and the number of corrupted records is growing with each nightly sync run.

## Root Cause Hypothesis
The sync pipeline's phone number parsing logic is truncating country codes to at most 2 digits. A change introduced approximately 2-3 weeks ago likely altered phone number formatting or validation — possibly a library update, a regex change, or a new normalization step that assumes country codes are 1-2 digits.

## Reproduction Steps
  1. Identify a customer record in Salesforce with a 3+ digit country code (e.g., +353 for Ireland)
  2. Confirm the number is correct in Salesforce
  3. Wait for (or manually trigger) the nightly sync
  4. Check the same record in TaskFlow — the country code will be truncated (e.g., +353 → +35)

## Environment
Nightly automated sync from Salesforce CRM to TaskFlow. Issue began approximately 2-3 weeks ago and is progressively affecting more records.

## Severity: high

## Impact
Support team cannot reach international customers by phone. Data integrity of customer contact records is degrading with each sync cycle. Approximately 50-100 records currently affected and growing nightly.

## Recommended Fix
1. Check git history for changes to the Salesforce sync's phone number parsing/formatting logic from ~3 weeks ago. 2. Look for country code validation that incorrectly constrains length to 2 digits, or a regex/substring operation that truncates the code. 3. Fix the parser to handle country codes of 1-3 digits per the E.164 standard. 4. Run a one-time re-sync or correction script to repair the ~50-100 already-corrupted records from the canonical Salesforce data.

## Proposed Test Case
Sync customer records with phone numbers using 1-digit (+1), 2-digit (+44), and 3-digit (+353, +370, +591) country codes from Salesforce. Verify all numbers are stored in TaskFlow exactly as they appear in Salesforce, with no truncation.

## Information Gaps
- Exact date the corruption began (reporter estimates 2-3 weeks; git history or sync logs can pinpoint this)
- Whether 2-digit country codes other than +44 are also safe (reporter only spot-checked UK)
- The specific code change or dependency update that introduced the regression
