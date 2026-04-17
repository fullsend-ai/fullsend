# Triage Summary

**Title:** File attachments corrupted on upload/download — size-dependent, regression from ~2 weeks ago

## Problem
PDF file attachments uploaded via the UI attach button are corrupted when downloaded. The corruption is immediate — files are broken on the first download attempt. The issue correlates with file size: small single-page PDFs appear to upload successfully, while larger multi-page documents (contracts, reports, scanned documents) are consistently corrupted. There is an unconfirmed report that a spreadsheet was also affected, suggesting the issue may not be PDF-specific.

## Root Cause Hypothesis
A change deployed approximately 1–2 weeks ago likely introduced a regression in the file upload pipeline that truncates, re-encodes, or otherwise corrupts files above a certain size threshold. Possible causes include: a new file size limit or chunked upload change that silently truncates large files, a middleware or proxy change (e.g., request body size limit), a binary-to-text encoding error (e.g., base64 or multipart boundary handling) that only manifests on larger payloads, or a storage layer change that corrupts writes above a certain size.

## Reproduction Steps
  1. Prepare a multi-page PDF file (e.g., a 5–10 page contract or report)
  2. Log into TaskFlow and navigate to any task
  3. Click the attach button in the UI and upload the PDF
  4. Immediately download the uploaded attachment
  5. Attempt to open the downloaded file — it should be reported as damaged/corrupted
  6. Repeat with a simple one-page PDF — this one should open successfully
  7. Compare the file sizes of the original and downloaded copies to check for truncation

## Environment
TaskFlow web UI, attach button upload flow. No specific browser/OS reported — issue appears server-side given the size-dependent pattern. Production environment, affecting multiple customers.

## Severity: high

## Impact
Multiple customers are unable to use file attachments for standard business documents (contracts, reports). The team relies heavily on this feature and their workflow is blocked. Only trivially small files are getting through.

## Recommended Fix
1. Review all commits/deployments from the past 2 weeks that touched the file upload pipeline, storage layer, or any proxy/middleware configuration. 2. Compare the byte content of an uploaded-then-downloaded file against the original to determine the nature of the corruption (truncation, encoding mangling, partial write). 3. Check for recently introduced file size limits, request body limits (e.g., nginx client_max_body_size, API gateway limits), or changes to multipart form handling. 4. Check if chunked upload or streaming logic was modified. 5. Test with non-PDF files of similar size to confirm whether the issue is size-dependent or format-dependent.

## Proposed Test Case
Upload files of increasing sizes (100KB, 500KB, 1MB, 5MB, 10MB) in multiple formats (PDF, XLSX, DOCX, PNG) via the UI attach button. Download each immediately and verify byte-for-byte integrity against the original using checksums. The test should identify the exact size threshold at which corruption begins and whether it is format-dependent.

## Information Gaps
- Exact file size threshold where corruption begins (developer can determine via systematic testing)
- Whether non-PDF file types are equally affected (one unconfirmed report of a corrupt spreadsheet)
- Nature of the corruption — truncation vs. encoding error vs. something else (developer can determine by comparing original and downloaded bytes)
- Specific browser and OS versions in use (likely irrelevant given the server-side, size-dependent pattern)
