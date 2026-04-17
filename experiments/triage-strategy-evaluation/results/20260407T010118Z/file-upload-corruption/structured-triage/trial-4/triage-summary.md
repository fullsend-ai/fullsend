# Triage Summary

**Title:** PDF attachments corrupted after upload — likely size-dependent regression

## Problem
PDF files uploaded via the task detail view attachment feature are corrupted and cannot be opened. The PDF viewer reports the file as damaged. This affects multiple users across different computers and started approximately one week ago. Smaller PDFs appear to upload successfully; larger ones (reports, contracts) are more likely to be corrupted.

## Root Cause Hypothesis
A recent change (approximately one week ago) likely introduced a regression in the file upload pipeline that truncates or corrupts larger uploads. Possible causes include: a misconfigured upload size limit or chunking change, a encoding/transfer issue (e.g., binary file not handled as binary), or a change in the storage backend or processing middleware that damages files above a certain size threshold.

## Reproduction Steps
  1. Open a task in the task detail view
  2. Click the paperclip icon to attach a file
  3. Upload a PDF file (try one that is several MB — reports or contracts)
  4. After upload completes, download the attached file
  5. Attempt to open the downloaded PDF — it should report as damaged/corrupted
  6. Repeat with a very small PDF (< 1 page) to confirm it uploads correctly

## Environment
Chrome (latest) reported, but issue reproduces across multiple users and different computers, suggesting it is not browser- or OS-specific. TaskFlow version unknown — check what was deployed approximately one week ago.

## Severity: high

## Impact
Multiple customers are affected. Teams that rely on file attachments for sharing reports and contracts cannot use the feature reliably, forcing workarounds and disrupting workflows.

## Recommended Fix
Review all changes to the file upload pipeline, storage backend, and any related middleware or proxy configuration deployed in the past 1-2 weeks. Specifically investigate: (1) any change to multipart upload handling or chunked transfer encoding, (2) upload size limits or body-parser configuration changes, (3) binary vs. text encoding in the upload stream, (4) CDN or reverse proxy changes that may alter request bodies above a size threshold. Compare byte-for-byte a corrupted uploaded file against its original to identify where truncation or corruption occurs.

## Proposed Test Case
Upload PDF files of varying sizes (100KB, 1MB, 5MB, 10MB) via the task detail view and verify that each downloaded attachment is byte-identical to the original. This should be an automated integration test that compares SHA-256 hashes of the original and retrieved files.

## Information Gaps
- Exact file size threshold where corruption begins
- Whether non-PDF file types (images, DOCX) are also affected
- Exact TaskFlow version / recent deployment that correlates with the onset
