# Triage Summary

**Title:** PDF file uploads corrupted since ~1 week ago (files won't open, size appears unchanged)

## Problem
PDF files uploaded to tasks via the file attachment feature are being corrupted during upload or storage. Downloaded files cannot be opened — PDF viewers report them as damaged. The issue started approximately one week ago and affects multiple customers. The corrupted files appear to be roughly the same size as the originals, suggesting the data is being altered rather than truncated. Other binary file types (images) may also be affected.

## Root Cause Hypothesis
A recent change to the file upload/storage pipeline (within the past 1–2 weeks) is corrupting binary file content while preserving file size. Likely candidates: a change in transfer encoding (e.g., binary being processed as text/UTF-8, introducing byte substitutions), a middleware or proxy change that re-encodes request bodies, a content-type handling change, or a storage layer change that mangles binary streams. The size-preserving nature points toward character encoding corruption rather than truncation or partial upload.

## Reproduction Steps
  1. Upload a known-good PDF (≥1 MB) to a task via the file attachment feature
  2. Download the PDF back from the task
  3. Attempt to open the downloaded PDF — expect it to be reported as damaged
  4. Compare the binary content of the original and downloaded files (e.g., diff at hex level) to identify the corruption pattern

## Environment
Server-side issue affecting all users; not browser- or OS-specific. Onset approximately one week ago.

## Severity: high

## Impact
Multiple customers are unable to share PDF documents through TaskFlow, blocking their workflows with their own customers. The file attachment feature is a core workflow for these users.

## Recommended Fix
1. Review all changes to the file upload pipeline, storage layer, and any reverse proxy or middleware configuration from the past two weeks. 2. Upload a test PDF and compare the stored/downloaded bytes against the original to identify the corruption pattern (byte substitutions suggest encoding issues; shifted bytes suggest framing issues). 3. Check for recent changes to content-type handling, transfer-encoding, or body parsing middleware. 4. If a CDN or proxy sits in front of storage, check for recent configuration changes there as well.

## Proposed Test Case
Integration test that uploads a binary PDF file, downloads it back, and asserts byte-for-byte equality with the original. Include files at multiple sizes (100 KB, 5 MB, 20 MB) to catch size-dependent issues.

## Information Gaps
- Exact file size threshold (if any) below which uploads succeed — reporter indicated smaller files might be okay but wasn't certain
- Whether non-PDF binary files (images, Word docs) are also affected — one unconfirmed report of a weird image
- Exact date the issue started — reporter estimated 'about a week ago'
- Whether the issue affects uploads via API (if applicable) or only the web UI
