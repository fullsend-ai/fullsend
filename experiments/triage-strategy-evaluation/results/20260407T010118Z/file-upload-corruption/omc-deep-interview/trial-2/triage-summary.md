# Triage Summary

**Title:** File uploads corrupted for larger files (PDFs, possibly images) since ~1 week ago

## Problem
PDF files uploaded to tasks via the web interface are completely unreadable when downloaded. The corruption affects larger files (multi-page contracts, reports with graphics) while small single-page PDFs appear unaffected. At least one large image upload was also reported as looking wrong. The issue started approximately one week ago and affects multiple customers.

## Root Cause Hypothesis
A recent change (~1 week ago) in the file upload pipeline is corrupting files above a certain size threshold. Since files are entirely unreadable (not truncated), this points to an encoding, content-type handling, or multipart reassembly issue rather than simple truncation. The size-dependent nature suggests chunked upload processing or a middleware change that mishandles larger payloads. Check deployments and infrastructure changes from ~1 week ago — likely a backend change to upload handling, a proxy/CDN configuration change, or a storage layer update.

## Reproduction Steps
  1. Log into TaskFlow web interface using Chrome
  2. Navigate to any task and use the file attachment feature
  3. Upload a multi-page PDF file (a few MB — e.g., a 5+ page contract or report with graphics)
  4. After upload completes, download the file back from the task
  5. Attempt to open the downloaded PDF — it should be completely unreadable/corrupted
  6. Repeat with a small single-page PDF to confirm it uploads and downloads correctly

## Environment
Web interface, Chrome browser. Issue is not browser-specific as far as we know (only Chrome confirmed). Affects production environment for multiple customers.

## Severity: high

## Impact
Multiple customers are unable to use file attachments for larger documents, which is a core workflow. This has been ongoing for approximately one week. Teams that rely heavily on PDF attachments for contracts and reports are significantly impacted in their daily operations.

## Recommended Fix
1. Review all deployments and infrastructure changes from ~1 week ago affecting the upload pipeline, file storage, or any reverse proxy/CDN in the upload path. 2. Compare the binary content of an uploaded file in storage against the original — check for encoding corruption (e.g., base64 double-encoding, charset mangling, or incorrect Content-Type handling). 3. Check if chunked transfer encoding or multipart upload reassembly was changed. 4. Test with files of varying sizes to identify the exact size threshold where corruption begins. 5. Verify the storage layer (S3, GCS, etc.) is receiving and returning files with correct content-type and no transformation.

## Proposed Test Case
Upload files of increasing sizes (100KB, 500KB, 1MB, 2MB, 5MB, 10MB) in both PDF and image formats. Download each and perform a byte-for-byte comparison (checksum) against the original. This identifies the exact size threshold and confirms whether the issue is format-specific or general. Add an automated integration test that uploads a file above the threshold and asserts the downloaded file's checksum matches the original.

## Information Gaps
- Exact file size threshold where corruption begins
- Whether non-Chrome browsers are also affected
- Whether API/integration uploads (not just web UI) are also affected
- The specific deployment or infrastructure change from ~1 week ago that caused this
- Whether the file is already corrupted in storage or only corrupted on download
