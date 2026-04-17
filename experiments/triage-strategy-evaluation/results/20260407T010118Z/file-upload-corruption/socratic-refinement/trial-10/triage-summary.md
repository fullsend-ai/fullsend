# Triage Summary

**Title:** Large PDF attachments silently corrupted on upload — small PDFs unaffected

## Problem
PDF files attached to tasks via the web interface upload successfully (no errors, progress bar completes normally), but when downloaded, larger PDFs are damaged and cannot be opened. Smaller PDFs (e.g., single-page documents) appear unaffected. Multiple customers are impacted. The issue started approximately one week ago with no client-side changes.

## Root Cause Hypothesis
A server-side change introduced roughly one week ago is corrupting large file uploads during processing or storage. Likely candidates: (1) a regression in chunked upload reassembly that truncates or misorders chunks for files exceeding a threshold, (2) a newly introduced or lowered request body size limit (e.g., in a reverse proxy, API gateway, or web server config) that silently truncates the upload, or (3) a timeout in an upload processing pipeline that cuts off large files before they finish writing to storage.

## Reproduction Steps
  1. Upload a PDF larger than ~1-2 MB to a task via the web interface
  2. Wait for the upload to complete (should show success, no errors)
  3. Download the uploaded file
  4. Attempt to open the downloaded PDF — it should be reported as damaged/corrupted
  5. Repeat with a small single-page PDF to confirm it uploads and downloads correctly

## Environment
Web interface (browser-based upload). No API or email integration involved. No recent changes on the client side (browser, PDF generation). Issue began approximately one week ago.

## Severity: high

## Impact
Multiple customers are unable to exchange PDF documents (contracts, reports) through the task attachment workflow. The reporter's team relies heavily on this feature and has customers waiting on documents. Silent corruption (no error feedback) means users only discover the problem after downloading, wasting time and eroding trust.

## Recommended Fix
1. Review all server-side changes deployed in the past 1-2 weeks that touch the file upload pipeline, storage layer, or reverse proxy/gateway configuration. 2. Check for recently introduced or changed request size limits (nginx client_max_body_size, API gateway payload limits, application-level multipart limits). 3. Inspect chunked upload handling — verify that all chunks are reassembled in order and completely for large files. 4. Compare the byte content of an uploaded-then-downloaded large PDF against the original to identify where truncation or corruption occurs (beginning, end, or mid-file). 5. Check upload processing timeouts that might cut off writes for larger files.

## Proposed Test Case
Upload PDFs of varying sizes (100KB, 1MB, 5MB, 10MB, 25MB) via the web interface, then download each and perform a byte-for-byte comparison (e.g., checksum) against the original. The test passes when all downloaded files are identical to their originals. Additionally, add an integration test that uploads a file above the suspected size threshold and asserts the downloaded content matches.

## Information Gaps
- Exact file size threshold where corruption begins (developers can determine this through controlled testing)
- Whether the corruption is deterministic (same file always corrupts the same way) or non-deterministic
- Whether the stored file on the server/object store is already corrupted, or only the download path introduces corruption
- Specific server-side deployments or configuration changes made in the past 1-2 weeks
