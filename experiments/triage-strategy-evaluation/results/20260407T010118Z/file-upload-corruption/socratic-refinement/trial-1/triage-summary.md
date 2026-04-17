# Triage Summary

**Title:** Large file attachments corrupted after upload/download — started ~1 week ago, affects PDFs and images

## Problem
Files uploaded via the task attachment feature are corrupted when downloaded. The corruption correlates with file size: larger files (multi-page PDFs, several MB) are consistently broken, while small single-page files appear to come through intact. At least one image upload has also been affected, indicating this is not PDF-specific. No errors are shown during upload. The issue began approximately one week ago with no client-side changes.

## Root Cause Hypothesis
A server-side or infrastructure change approximately one week ago is corrupting larger file uploads or downloads. Likely candidates: (1) a change in multipart upload handling that truncates or re-encodes files above a certain size, (2) a reverse proxy, CDN, or gateway configuration change introducing a body size limit or timeout that silently truncates responses, (3) a change in content-type or transfer-encoding handling (e.g., binary vs base64) that mangles non-text payloads, or (4) a storage layer change affecting how file blobs are written or read.

## Reproduction Steps
  1. Upload a multi-page PDF (~2-5 MB) to a task using the standard upload button
  2. Observe that the upload completes without errors
  3. Download the same file from the task
  4. Attempt to open the downloaded PDF — it will be reported as damaged/corrupted
  5. Repeat with a small single-page PDF (~100 KB) — this may succeed
  6. Repeat with a larger image file to confirm cross-format corruption

## Environment
Multiple customers affected, standard upload button used, no specific browser/OS identified but issue is cross-user. Production environment, onset approximately 2026-03-31.

## Severity: high

## Impact
Teams that rely on file attachments for client-facing document sharing are blocked. Multiple customers affected. Workaround is limited to sharing only very small files.

## Recommended Fix
1. Review all deployments and infrastructure changes from approximately one week ago (late March 2026). 2. Compare byte-for-byte a known-corrupted uploaded file against its original to identify the corruption pattern (truncation, encoding change, null bytes, etc.). 3. Check upload/download pipeline for size-dependent behavior: proxy body limits, chunked transfer handling, streaming vs buffering thresholds. 4. Examine content-type and transfer-encoding headers for uploaded vs downloaded files. 5. Check storage layer (S3, blob store, etc.) for any configuration or library version changes.

## Proposed Test Case
Upload files of varying sizes (100KB, 1MB, 5MB, 10MB) across multiple formats (PDF, PNG, DOCX). Download each and verify byte-for-byte integrity against the original. Identify the exact size threshold where corruption begins. This should be automated as a regression test for the upload/download pipeline.

## Information Gaps
- Exact file size threshold where corruption begins
- Specific browser and OS versions (though unlikely to be relevant given cross-user scope)
- Whether the corruption is truncation, encoding mangling, or something else (dev can check server-side)
- Exact nature of the image corruption (similar pattern or different issue)
