# Triage Summary

**Title:** PDF attachments corrupted on upload above a certain file size (regression ~1 week ago)

## Problem
PDF files attached to tasks are being corrupted during upload, rendering them unopenable. The corruption appears to affect larger PDFs while smaller ones upload successfully. Multiple customers are impacted. The issue began approximately one week ago, suggesting a regression introduced by a recent change.

## Root Cause Hypothesis
A recent change (~1 week ago) introduced a size-dependent failure in the file upload pipeline. Likely candidates: (1) a new upload size limit or request body size limit that silently truncates larger files rather than rejecting them, (2) a change to chunked upload/multipart handling that corrupts reassembly for files above a threshold, or (3) a middleware or proxy configuration change (e.g., nginx client_max_body_size, API gateway timeout) that truncates the request body.

## Reproduction Steps
  1. Upload a small PDF (under 1 MB) to a task — confirm it downloads and opens correctly
  2. Upload a larger PDF (5+ MB) to a task
  3. Download the uploaded PDF and attempt to open it
  4. Compare the file sizes of the original and downloaded PDF to determine if truncation occurred
  5. Check if the corruption is truncation (file is smaller) or encoding damage (file is same size but garbled)

## Environment
Production environment, multiple customers affected, started approximately one week ago

## Severity: high

## Impact
Multiple customers cannot use PDF attachments for larger files, blocking workflows that depend on file sharing within tasks. The feature is described as heavily used.

## Recommended Fix
1. Review deployment history and changelog from ~1 week ago for changes to upload handling, file storage, middleware, or proxy configuration. 2. Check server-side upload logs for errors or warnings on failed/large uploads. 3. Determine the size threshold by uploading test PDFs of increasing size in a staging environment. 4. Compare uploaded vs. stored file sizes in the storage backend to confirm whether truncation is occurring. 5. Check for recent changes to request body size limits (nginx, API gateway, application-level multipart config).

## Proposed Test Case
Upload PDFs at various sizes (1 MB, 5 MB, 10 MB, 25 MB) and verify each can be downloaded and opened without corruption. Automate by comparing SHA-256 hashes of the original and downloaded files to ensure byte-for-byte integrity.

## Information Gaps
- Exact file size threshold where corruption begins (determinable via server-side testing)
- Whether corruption is truncation or encoding damage (determinable by comparing original vs. downloaded file sizes)
- Specific deployment or config change that coincides with the ~1 week ago onset (determinable from deploy logs)
