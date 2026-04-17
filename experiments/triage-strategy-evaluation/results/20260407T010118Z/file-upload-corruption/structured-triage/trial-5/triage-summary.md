# Triage Summary

**Title:** PDF attachments corrupted on upload via drag-and-drop — likely size-dependent

## Problem
PDF files uploaded as task attachments via drag-and-drop in the task detail view are corrupted and cannot be opened. The PDF viewer reports the file is damaged. The issue affects multiple customers across different browsers and operating systems. Smaller PDFs appear to upload successfully; larger ones are consistently corrupted.

## Root Cause Hypothesis
A size-dependent corruption suggests a chunked upload, streaming, or encoding issue — likely a regression introduced approximately one week ago. Possible causes include: a server-side change truncating or re-encoding the upload stream for files exceeding a buffer/chunk boundary, a misconfigured multipart upload threshold, or a broken content-transfer-encoding (e.g., binary file being processed as text/UTF-8). The one-week timeline points to a recent deployment or infrastructure change.

## Reproduction Steps
  1. Open a task in the task detail view
  2. Drag and drop a PDF file (larger than ~1-2 MB) onto the attachment area
  3. Wait for the upload to complete
  4. Download the uploaded PDF
  5. Attempt to open the downloaded PDF — it will report as damaged/corrupted

## Environment
Cross-browser (Chrome, Firefox) and cross-platform (macOS, Windows). Not browser- or OS-specific. Regression started approximately one week ago.

## Severity: high

## Impact
Multiple customers are unable to exchange PDF documents through the task attachment feature. This blocks workflows that depend on document sharing and has been ongoing for approximately one week.

## Recommended Fix
1. Check deployment history for changes ~1 week ago affecting file upload, storage, or streaming code. 2. Compare a corrupted uploaded file byte-for-byte against the original to identify where corruption occurs (truncation, encoding, partial write). 3. Investigate whether there is a file-size threshold (e.g., multipart chunking boundary) that triggers the corruption. 4. Check server upload handling for binary vs. text encoding issues (e.g., ensure files are streamed as application/octet-stream, not re-encoded).

## Proposed Test Case
Upload PDF files of varying sizes (e.g., 100 KB, 1 MB, 5 MB, 20 MB) via drag-and-drop in the task detail view. Download each and verify the downloaded file is byte-identical to the original (checksum comparison). The test should confirm that files above and below the corruption threshold all survive the round trip intact.

## Information Gaps
- Exact file size threshold where corruption begins
- Whether the corruption is truncation, encoding mangling, or something else (requires server-side binary comparison)
- Specific deployment or infrastructure change that coincided with the onset ~1 week ago
