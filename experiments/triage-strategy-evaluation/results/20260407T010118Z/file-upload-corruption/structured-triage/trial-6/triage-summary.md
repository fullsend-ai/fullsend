# Triage Summary

**Title:** PDF attachments corrupted during upload/download — regression in last 1-2 weeks

## Problem
PDF files uploaded via the task attachment button are corrupted when subsequently downloaded. The PDF viewer reports the file as damaged or unable to be opened. This affects multiple customers and is a regression — the feature was working correctly until approximately 1-2 weeks ago.

## Root Cause Hypothesis
A recent backend change (within the last 1-2 weeks) is corrupting PDF files during the upload processing or storage pipeline. Likely candidates include: a change in encoding/transfer encoding (e.g., binary vs. base64), a file streaming bug that truncates or mangles content, a middleware change affecting multipart form uploads, or a storage layer change that alters file bytes.

## Reproduction Steps
  1. Open a task in TaskFlow
  2. Click the attachment button
  3. Select a known-good PDF file from the local filesystem
  4. Upload the file
  5. Download the uploaded attachment
  6. Attempt to open the downloaded PDF in any PDF viewer — it will report the file as damaged or corrupted

## Environment
Browser: Chrome (latest version). OS and TaskFlow version unknown. Issue is reported across multiple customers, suggesting a server-side problem independent of client environment.

## Severity: high

## Impact
Multiple customers are affected. Teams that rely heavily on PDF attachments (reporter's team uploads dozens per day) have lost a core workflow capability. No workaround is available.

## Recommended Fix
1. Review all backend deployments from the last 2 weeks for changes to the file upload, storage, or download pipeline. 2. Upload a test PDF and compare the stored bytes against the original to identify where corruption occurs (upload processing, storage, or download serving). 3. Check for changes in content-type handling, transfer encoding, streaming/chunking logic, or middleware affecting multipart uploads. 4. Verify that binary files are not being inadvertently processed as text (e.g., charset conversion or line-ending normalization).

## Proposed Test Case
Upload a PDF of known size and checksum via the attachment button. Download it and verify the checksum matches the original. Test with multiple file sizes (small <100KB, medium ~1MB, large >10MB) to check for size-dependent corruption patterns.

## Information Gaps
- Exact OS of the reporter (asked but not provided)
- TaskFlow version (reporter indicated this is managed by another team)
- Whether non-PDF file types are also affected
- Whether the corruption is consistent (same bytes corrupted every time) or variable
- Exact file sizes involved
- Whether the issue is specific to uploading or also affects previously-uploaded PDFs
