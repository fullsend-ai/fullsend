# Triage Summary

**Title:** Large/multi-page PDF attachments corrupted after upload (regression ~1 week ago)

## Problem
PDF files uploaded via the task detail view attach button are corrupted when downloaded. The corruption appears to affect larger, multi-page, or image-heavy PDFs, while small single-page PDFs upload and download correctly. The upload process itself shows no errors. This is a regression that began approximately one week ago.

## Root Cause Hypothesis
A recent change (approximately one week ago) likely introduced a problem in the file upload or storage pipeline that truncates or corrupts files above a certain size. Possible causes include: a misconfigured upload size limit or chunked upload handling, a change in encoding or content-type handling for binary files, or a storage layer change that corrupts multi-part uploads. The size-dependent nature strongly suggests a chunking, streaming, or buffer size issue.

## Reproduction Steps
  1. Open a task in the task detail view
  2. Click the attach button
  3. Select a multi-page PDF file (e.g., 5+ pages with embedded images)
  4. Upload completes with no visible errors
  5. Download the uploaded file
  6. Attempt to open the downloaded PDF — it will be reported as damaged/corrupted
  7. Repeat with a small single-page PDF to confirm it works correctly

## Environment
Chrome (latest) on Windows 10. Confirmed across multiple team members and not browser-specific.

## Severity: high

## Impact
Multiple customers and internal team members are unable to use PDF attachments for anything beyond simple single-page documents. The file attachment feature is heavily used in their workflow, making this a significant productivity blocker.

## Recommended Fix
1. Check deployment/change history from approximately one week ago for changes to the file upload pipeline, storage configuration, or any infrastructure changes (e.g., reverse proxy body size limits, API gateway timeouts, storage SDK updates). 2. Compare the byte size of a known-good original PDF with its uploaded/downloaded counterpart to determine if data is being truncated or altered. 3. Inspect upload handling for chunked transfer or streaming issues — check whether large files are being fully read before storage. 4. Check content-type and transfer-encoding headers for binary file handling.

## Proposed Test Case
Upload PDFs of varying sizes (e.g., 100KB single-page, 2MB multi-page, 10MB image-heavy) and verify that a byte-for-byte comparison (checksum) of the original and downloaded files matches for all sizes.

## Information Gaps
- Exact file size threshold where corruption begins
- Whether non-PDF file types (images, Word docs) are also affected
- Server-side logs from the upload and download requests
- Exact deployment or configuration change from ~1 week ago
