# Triage Summary

**Title:** Phone number country codes truncated to 2 digits during Salesforce batch import

## Problem
The nightly batch import from Salesforce is truncating international phone number country codes to 2 digits. Numbers with 1- or 2-digit country codes (+1, +44) are unaffected, but longer codes like +353 (Ireland) become +35 and +1268 (Antigua) becomes +12. Approximately 50-100 of ~2,000 customer records are affected.

## Root Cause Hypothesis
A recent change to phone number formatting or validation (likely in the v2.3.1 timeframe) introduced a hard limit of 2 characters for the country code field during import parsing. The parsing logic likely splits the phone number assuming all country codes are 1-2 digits, truncating 3- and 4-digit codes.

## Reproduction Steps
  1. Ensure a Salesforce record exists with a phone number using a 3+ digit country code (e.g., +353 1 234 5678 for Ireland)
  2. Run the nightly batch import sync from Salesforce
  3. Check the imported record in TaskFlow — the country code will be truncated to 2 digits (+35 1 234 5678)

## Environment
TaskFlow v2.3.1, nightly batch import from Salesforce CRM

## Severity: high

## Impact
50-100 customer contact records have corrupted phone numbers. Support team cannot reach affected customers. Data corruption is ongoing with each nightly import, potentially re-corrupting any manual fixes.

## Recommended Fix
Review the phone number formatting/validation change introduced ~2 weeks ago (likely in v2.3.1 or a recent deployment). Look for a hard-coded country code length limit or incorrect substring/slice operation in the import pipeline's phone number parser. Country codes range from 1 to 4 digits per the E.164 standard. After fixing, run a one-time repair job to re-import affected numbers from Salesforce.

## Proposed Test Case
Unit test the phone number import parser with country codes of varying lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4 digits). Assert that the full country code and subscriber number are preserved in each case.

## Information Gaps
- Exact commit or changeset that introduced the formatting/validation change
- Whether any error logs are generated during import for the affected numbers
- Whether manually entered phone numbers with long country codes are also affected (UI path vs import path)
