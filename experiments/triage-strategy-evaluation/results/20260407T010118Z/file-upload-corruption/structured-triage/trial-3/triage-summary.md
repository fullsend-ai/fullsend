# Triage Summary

**Title:** PDF attachments corrupted on download — size-dependent, regression in ~2.3.x

## Problem
PDF files uploaded via the 'Attach file' button in task view are corrupted when downloaded by clicking the attachment link. The PDF viewer reports the file as damaged. Smaller PDFs appear unaffected; larger PDFs (multi-page reports, contracts) are consistently broken. The issue affects multiple customers and began approximately one week ago.

## Root Cause Hypothesis
A recent change in the ~2.3.x release likely introduced a bug in the file upload or download pipeline that corrupts larger files. Probable causes include: (1) a multipart/chunked upload reassembly bug truncating or misordering chunks, (2) an incorrect Content-Length or Transfer-Encoding header on download causing truncation, (3) a new file-size limit or middleware (e.g., compression, virus scanning) silently corrupting files above a threshold, or (4) a binary-vs-text encoding issue (e.g., base64 or UTF-8 transcoding applied to binary data) that only manifests when the file exceeds a buffer size.

## Reproduction Steps
  1. Open a task in TaskFlow 2.3.x
  2. Click the 'Attach file' button
  3. Upload a PDF larger than ~1-2 MB (e.g., a multi-page report or contract)
  4. After upload completes, click the attachment link to download the file
  5. Attempt to open the downloaded PDF — it should report as damaged/corrupted
  6. Repeat with a very small PDF (<100 KB) to confirm it downloads correctly

## Environment
TaskFlow ~2.3.x (recent update, approximately one week old), Chrome and Firefox, multiple customers affected

## Severity: high

## Impact
Multiple customers who rely on file attachments cannot share or retrieve PDF documents, blocking daily workflows for entire teams.

## Recommended Fix
1. Check recent changes (past ~1-2 weeks) to the file upload/download pipeline, storage layer, or any middleware (compression, antivirus, CDN config). 2. Compare the byte content of an uploaded large PDF in storage against the original — identify where corruption occurs (upload vs. storage vs. download). 3. Check for chunked transfer or multipart upload changes. 4. Verify Content-Type and Content-Length headers on download responses. 5. Check for any new file-size limits or transcoding logic.

## Proposed Test Case
Upload PDFs of varying sizes (100 KB, 1 MB, 5 MB, 20 MB) via the 'Attach file' button, download each via the attachment link, and verify the downloaded file's SHA-256 checksum matches the original. This should be an automated integration test that covers the full upload-store-download round trip.

## Information Gaps
- Exact file sizes that trigger corruption (threshold is unclear beyond 'a few MB')
- Exact TaskFlow version (reporter said '2.3 something')
- Whether non-PDF file types are also affected
- Server-side error logs from the upload/download endpoints
