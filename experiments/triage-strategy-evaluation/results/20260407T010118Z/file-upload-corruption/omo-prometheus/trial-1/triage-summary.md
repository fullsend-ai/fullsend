# Triage Summary

**Title:** PDF attachments corrupted on upload for files in ~2-5 MB range (regression, ~1 week)

## Problem
PDF files uploaded to tasks are immediately corrupted — downloading them right after upload yields a damaged file that PDF viewers cannot open. The issue affects most customer-uploaded PDFs, which tend to be in the 2-5 MB range. Smaller PDFs appear to upload correctly. This is a regression that began approximately one week ago.

## Root Cause Hypothesis
A recent change (likely deployed ~1 week ago) introduced a bug in the file upload path that affects files above a certain size threshold. Likely candidates: (1) a chunked/multipart upload boundary that kicks in at a size threshold is corrupting the reassembled file, (2) an encoding or content-type handling change (e.g., base64 encoding binary data, or incorrect Transfer-Encoding) that mangles larger payloads, or (3) a middleware or proxy change (e.g., request body size limit, compression, or streaming behavior) that truncates or transforms the upload stream. The size correlation strongly suggests the upload code path diverges based on file size.

## Reproduction Steps
  1. Obtain any PDF file in the 2-5 MB range (a multi-page report or scanned document works)
  2. Create or open a task in TaskFlow
  3. Upload the PDF using the file attachment feature
  4. Immediately download the uploaded file
  5. Attempt to open the downloaded PDF — it should be reported as damaged/corrupted
  6. Repeat with a small PDF (~100 KB) to confirm small files are unaffected

## Environment
Not browser- or OS-specific per the report (multiple customers affected). Server-side issue. Check deployment history for changes in the past ~7-10 days.

## Severity: high

## Impact
Multiple customers are affected daily. The support team is fielding complaints. The file attachment feature is core to the reporter's workflow with their customers. No known workaround exists besides avoiding PDF uploads.

## Recommended Fix
1. Review all deployments and code changes from the past 7-10 days touching: file upload endpoints, multipart handling, storage service integration, middleware (body parsers, compression, proxies), and any infrastructure changes (CDN, load balancer, object storage config). 2. Reproduce locally by uploading a 2-5 MB PDF. 3. Compare the byte content of the original file with the stored/downloaded copy — look for truncation, encoding artifacts (e.g., base64 wrapping of binary), or injection of extra bytes. 4. Check if there is a file-size threshold in the upload code where handling diverges (e.g., streaming vs. buffered, single-part vs. multipart). 5. Verify Content-Type headers are preserved as application/octet-stream or application/pdf through the full upload pipeline.

## Proposed Test Case
Upload PDFs of varying sizes (100 KB, 1 MB, 2 MB, 5 MB, 10 MB) and verify byte-for-byte integrity by comparing SHA-256 checksums of the original and downloaded files. The test should fail if any checksum differs. Add this as an integration test against the upload endpoint.

## Information Gaps
- Whether non-PDF file types are also affected (reporter only uses PDFs — developers should test other types during reproduction)
- Exact size threshold where corruption begins
- Whether the stored file on the server/object storage is already corrupted or if corruption happens during download
- Exact byte-level nature of the corruption (truncation vs. encoding vs. data transformation)
