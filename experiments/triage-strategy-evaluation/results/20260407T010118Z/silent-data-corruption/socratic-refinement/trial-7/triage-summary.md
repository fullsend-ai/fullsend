# Triage Summary

**Title:** Phone number country codes truncated to 2 digits during Salesforce nightly sync after recent international format change

## Problem
Customer phone numbers with country codes longer than 2 digits (e.g., Ireland +353, Caribbean nations) are being truncated to 2 digits during the nightly Salesforce-to-TaskFlow batch import. Approximately 50-100 of ~2,000 records are affected. US (+1) and UK (+44) numbers are unaffected.

## Root Cause Hypothesis
A recent change (~2 weeks ago) to phone number normalization/parsing in the import sync job likely hardcodes or truncates the country code field to a maximum of 2 characters. This would explain why +1 and +44 pass through correctly while +353 becomes +35.

## Reproduction Steps
  1. Ensure a Salesforce record exists with a phone number using a 3-digit country code (e.g., +353 1 234 5678 for Ireland)
  2. Run the nightly batch import (or trigger it manually)
  3. Inspect the resulting phone number in TaskFlow — the country code will be truncated to 2 digits (+35 1 234 5678)

## Environment
Salesforce-to-TaskFlow nightly batch sync job; issue is in the import/normalization layer, not in Salesforce data or TaskFlow storage

## Severity: high

## Impact
Support team cannot reach affected international customers (~50-100 records). Data integrity issue that silently corrupts contact information on every sync run, meaning corrected records would be re-corrupted nightly until fixed.

## Recommended Fix
Find the recent commit (~2 weeks ago) that modified phone number normalization in the sync/import job. Look for code that parses or extracts the country code and likely slices or substrings it to 2 characters. Fix to handle variable-length country codes (1-3 digits per E.164). After fixing, re-sync affected records from Salesforce to restore correct numbers.

## Proposed Test Case
Unit test the phone number normalization function with country codes of varying lengths: +1 (1 digit, US), +44 (2 digits, UK), +353 (3 digits, Ireland), +1-268 (1 digit + area code, Antigua). Assert that the full country code and number are preserved in each case.

## Information Gaps
- Exact commit or code location of the recent phone number handling change (dev team can identify via git history)
- Whether the truncation also affects the Antigua numbers' area code or just the country code portion
- Whether already-corrupted records will self-heal on re-sync once the bug is fixed, or if a one-time data repair is needed
