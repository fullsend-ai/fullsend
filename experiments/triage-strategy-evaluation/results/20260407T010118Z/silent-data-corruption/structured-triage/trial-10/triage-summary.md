# Triage Summary

**Title:** Phone number country codes truncated to 2 digits during Salesforce batch import

## Problem
Customer phone numbers with country codes longer than two digits are being truncated to exactly two digits during the nightly Salesforce CRM batch import. Numbers with 1- or 2-digit country codes (e.g., US +1, UK +44) are unaffected. Approximately 50-100 of ~2,000 customer records are corrupted, and the nightly sync continues to overwrite them with truncated values.

## Root Cause Hypothesis
A recent change in the phone number parsing or storage logic in the Salesforce import pipeline is truncating the country code field to a maximum of 2 characters. This likely involves a field length constraint, substring operation, or schema change introduced in a TaskFlow update within the last ~2 weeks.

## Reproduction Steps
  1. Configure a Salesforce sync with customer records containing international phone numbers with country codes of varying lengths (e.g., +1, +44, +353, +1268)
  2. Run the batch import process
  3. Inspect the imported phone numbers in TaskFlow
  4. Observe that country codes longer than 2 digits are truncated to 2 digits (e.g., +353 becomes +35, +1268 becomes +12)

## Environment
TaskFlow v2.3.1, Salesforce CRM integration via nightly batch import, no recent changes to Salesforce sync configuration or field mappings on the customer side

## Severity: high

## Impact
Support team cannot reach customers with international numbers that have 3+ digit country codes. Data corruption is ongoing — each nightly sync overwrites correct numbers with truncated ones, compounding the damage. Approximately 50-100 records currently affected.

## Recommended Fix
Investigate the Salesforce import pipeline's phone number parsing logic, specifically any field length constraints or substring operations on the country code portion. Check git history and release notes for changes made ~2 weeks ago to the import module, phone number normalization, or database schema. Look for a column width change, a regex with a hard-coded capture group length, or a VARCHAR constraint on a country_code field.

## Proposed Test Case
Import phone numbers with country codes of length 1 (+1), 2 (+44), 3 (+353), and 4 (+1268) via the batch import pipeline and verify all digits are preserved in the stored records.

## Information Gaps
- Exact TaskFlow release/update history on the customer's instance over the past month
- Server-side import logs that might show warnings or data transformation details
- Whether the truncation happens at the API ingestion layer, a normalization step, or at database storage
