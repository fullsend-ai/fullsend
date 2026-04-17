# Triage Summary

**Title:** Large file uploads corrupted on download (PDFs confirmed, possibly other types)

## Problem
Files uploaded to tasks via the web app are corrupted when downloaded. The corruption is size-dependent: smaller PDFs work fine, but larger ones (reporter estimates 'a few MB' and up for business documents like contracts and reports) are broken and cannot be opened. An anecdotal report suggests at least one image upload may also have been affected. This is a regression — uploads were working correctly until approximately 1-2 weeks ago.

## Root Cause Hypothesis
A recent change (within the last 1-2 weeks) to the upload pipeline is corrupting larger files. The size-dependent nature and cross-format hints point to a general upload handling issue rather than PDF-specific processing — likely a bug in chunked upload assembly, multipart form handling, stream buffering, or a newly introduced file size limit that silently truncates rather than rejecting. A misconfigured reverse proxy (e.g., Nginx client_max_body_size) or a change to the storage layer (S3 multipart upload config) could also explain this.

## Reproduction Steps
  1. Upload a small PDF (under 1 MB) to a task via the web app — confirm it downloads correctly
  2. Upload a larger PDF (3-5 MB+, e.g., a multi-page scanned document or report) to a task via the web app
  3. Download the uploaded file and attempt to open it
  4. Compare the uploaded and downloaded file sizes and checksums (MD5/SHA) to identify truncation or byte corruption
  5. Repeat with a large image or other binary file to confirm whether corruption is format-independent

## Environment
Web application (browser not specified). Multiple customers affected. No information on specific browser, OS, or network conditions.

## Severity: high

## Impact
Multiple customers are blocked. Teams that rely heavily on file attachments (the reporter's primary workflow involves contracts and reports) cannot use the feature. This is a regression from a previously working state, affecting core functionality.

## Recommended Fix
1. Review all deployments and config changes from the past 2 weeks for anything touching the upload pipeline (upload endpoints, storage service, reverse proxy config, CDN settings). 2. Compare uploaded vs. downloaded file byte-for-byte at each stage of the pipeline (client → server → storage → retrieval) to identify where corruption is introduced. 3. Check for changes to multipart upload handling, chunked transfer encoding, stream processing, or file size limits. 4. Look at infrastructure changes (proxy/load balancer config, storage backend) that could silently truncate or re-encode binary data.

## Proposed Test Case
Upload files of varying sizes (100 KB, 1 MB, 5 MB, 10 MB, 50 MB) across multiple formats (PDF, PNG, DOCX, ZIP). After download, assert that the SHA-256 checksum of the downloaded file matches the original. This should be an automated integration test against the upload/download API.

## Information Gaps
- Exact file size threshold where corruption begins (developer can determine via systematic testing)
- Whether non-PDF file types are also affected (anecdotal hint suggests yes, but unconfirmed)
- Specific browser and OS used by affected customers
- Whether the stored file (in object storage / on disk) is already corrupted or if corruption happens on download
