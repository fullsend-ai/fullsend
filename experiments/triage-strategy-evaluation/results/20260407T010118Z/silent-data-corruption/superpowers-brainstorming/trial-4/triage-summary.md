# Triage Summary

**Title:** Salesforce sync truncates phone country codes longer than 2 digits

## Problem
The nightly Salesforce-to-TaskFlow customer sync is corrupting international phone numbers by truncating country calling codes to 2 digits. Countries with 1-2 digit codes (US +1, UK +44) are unaffected, but countries with 3+ digit codes (Ireland +353, Antigua +1268) are mangled. Roughly 50-100 of ~2,000 customer records are affected and the number grows with each nightly sync run.

## Root Cause Hypothesis
A change introduced approximately two weeks ago in the phone number parsing or storage path of the Salesforce sync is applying a 2-character limit to the country code portion of international phone numbers. This is likely a field-length constraint, substring operation, or regex pattern that assumes country codes are at most 2 digits.

## Reproduction Steps
  1. Identify a customer record in Salesforce with a country code longer than 2 digits (e.g., Ireland +353 1 234 5678)
  2. Wait for (or manually trigger) the nightly Salesforce sync
  3. Check the same customer record in TaskFlow — the country code will be truncated to 2 digits (e.g., +35 1 234 5678)

## Environment
Salesforce CRM → nightly sync integration → TaskFlow (production). Issue began approximately two weeks ago.

## Severity: high

## Impact
Support and outreach teams cannot reach international customers with 3+ digit country codes. Data corruption worsens with each nightly sync run, affecting an increasing number of records. Currently 50-100 records affected out of ~2,000.

## Recommended Fix
1. Check git history for changes to the Salesforce sync code from ~2 weeks ago, focusing on phone number parsing, formatting, or storage. 2. Look for a substring, field-length, or regex constraint that limits country codes to 2 characters. 3. Fix the parsing to accept country codes of 1-3 digits (per the ITU E.164 standard). 4. Run a one-time remediation to re-sync affected records from Salesforce to restore correct phone numbers.

## Proposed Test Case
Unit test the phone number parser with country codes of varying lengths: +1 (US, 1 digit), +44 (UK, 2 digits), +353 (Ireland, 3 digits), +1268 (Antigua, 4-digit with shared +1 prefix). Assert that full country codes and subscriber numbers are preserved without truncation.

## Information Gaps
- Exact code change or deployment that introduced the bug ~2 weeks ago (developer can find via git history)
- Whether the truncation happens during parsing, transmission, or storage (developer can trace through the sync pipeline)
