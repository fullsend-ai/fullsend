# Triage Summary

**Title:** Large PDF attachments are corrupted after upload — likely file size related

## Problem
PDF files uploaded as task attachments become corrupted and cannot be opened after download. The PDF viewer reports the file as damaged. This affects multiple customers and is causing daily workflow disruption. The issue appears to be correlated with file size — larger PDFs (e.g., customer reports) are consistently corrupted, while smaller internal PDFs upload and download fine.

## Root Cause Hypothesis
Large file uploads are being truncated or incorrectly processed, likely due to a file size limit in the upload pipeline (e.g., request body size limit in the web server, API gateway, or multipart upload handler), or a chunked transfer encoding issue where large files are not being fully reassembled before storage.

## Reproduction Steps
  1. Open a task detail page in TaskFlow
  2. Click the attachment button (or use drag-and-drop)
  3. Upload a large PDF file (e.g., a multi-page customer report — try files over a few MB)
  4. Wait for upload to complete
  5. Download the uploaded PDF
  6. Attempt to open the downloaded PDF — it will report as damaged/corrupted

## Environment
Chrome (latest) on Windows 10. TaskFlow version unknown. Issue reported by multiple customers, suggesting it is not environment-specific.

## Severity: high

## Impact
Multiple customers are affected daily. Teams relying on file attachments for customer-facing workflows cannot share PDF reports, causing direct customer-facing impact and angry customers.

## Recommended Fix
1. Compare uploaded vs. downloaded file sizes for a corrupted PDF to confirm truncation. 2. Check web server and reverse proxy configs for request body size limits (e.g., nginx client_max_body_size, API gateway payload limits). 3. Inspect the upload handler for chunked/multipart transfer issues with large files. 4. Check if the storage layer (S3, local disk, etc.) is receiving the complete file. 5. Verify Content-Length and Content-Type headers are correct on both upload and download paths.

## Proposed Test Case
Upload PDF files of increasing sizes (e.g., 1MB, 5MB, 10MB, 25MB, 50MB) and verify that each downloaded file is byte-identical to the original by comparing checksums (SHA-256). Also test with both drag-and-drop and file picker upload methods.

## Information Gaps
- Exact file size threshold where corruption begins
- Browser and TaskFlow version details (reporter declined to provide)
- Whether the issue occurs in other browsers or operating systems
- Server-side error logs during the upload of affected files
