# Triage Summary

**Title:** Large PDF attachments corrupted on upload (regression, ~1 week old)

## Problem
PDF files attached to tasks are corrupted and unopenable after upload. The issue selectively affects larger files (multi-page scans, image-heavy reports) while small single-page PDFs upload correctly. Multiple customers are affected. The behavior is a regression that began approximately one week ago.

## Root Cause Hypothesis
A recent change (within the last week) to the file upload pipeline is truncating or corrupting large files. Most likely candidates: a new file size limit was introduced, a request/upload timeout was reduced, chunked transfer encoding was changed, or a streaming/buffering change is cutting off uploads that exceed a certain size or duration.

## Reproduction Steps
  1. Upload a small, simple single-page PDF (e.g., <500 KB) to a task — expect success
  2. Upload a large multi-page or image-heavy PDF (e.g., >5 MB) to a task
  3. Download the uploaded large PDF and attempt to open it
  4. Observe that the file is reported as damaged/corrupted by the PDF viewer
  5. Compare the uploaded file size to the original to check for truncation

## Environment
Production environment, affects multiple customers. No specific browser or OS constraint reported — likely server-side.

## Severity: high

## Impact
Multiple customers are unable to use the file attachment feature for large PDFs. The team is receiving complaints and has been impacted for approximately one week. Core workflow (attaching documents to tasks) is partially broken.

## Recommended Fix
1. Review all deployments and config changes from the past ~10 days affecting the upload pipeline (file upload endpoints, reverse proxy config, storage layer). 2. Check for newly introduced file size limits, reduced timeouts, or changes to multipart/chunked upload handling. 3. Test uploading files at various sizes to identify the exact threshold where corruption begins. 4. Compare a corrupted uploaded file against the original (file size, binary diff) to determine if the file is truncated or garbled — this distinguishes timeout/size-limit issues from encoding issues. 5. If a specific change is identified, roll it back or fix the boundary condition.

## Proposed Test Case
Upload PDFs of increasing sizes (100 KB, 1 MB, 5 MB, 10 MB, 25 MB) and verify that each downloaded file matches the original byte-for-byte (SHA-256 checksum comparison). This test should cover both simple text PDFs and image-heavy scanned documents.

## Information Gaps
- Exact file size threshold where corruption begins
- Whether corrupted files are truncated (smaller than original) or garbled (same size but different content)
- Which specific deployment or config change introduced the regression
