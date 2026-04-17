# Triage Summary

**Title:** PDF attachments corrupted on download — size-dependent, regression from ~1 week ago

## Problem
PDF files attached to tasks via the standard attach button are corrupted when downloaded back from TaskFlow. The corruption appears to be size-dependent: small (e.g., one-page) PDFs may upload correctly, while larger PDFs are consistently broken. The PDF viewer reports the file as damaged. At least one report of an image also looking 'weird' after upload, suggesting the issue may not be PDF-specific.

## Root Cause Hypothesis
A recent change (~1 week ago) likely broke the file upload or download pipeline for files above a certain size. Probable causes: (1) a change to chunked upload/download handling that corrupts or truncates larger payloads, (2) an encoding or content-type change (e.g., incorrect Base64 encoding, missing binary mode) that mangles bytes beyond a buffer boundary, or (3) a storage or CDN configuration change that affects larger objects. The size-dependence strongly suggests a boundary condition in streaming, chunking, or buffering logic.

## Reproduction Steps
  1. Create or open an existing task in TaskFlow
  2. Attach a multi-page PDF (e.g., 5+ pages, several MB) using the attach button
  3. Download the attached PDF from the task
  4. Attempt to open the downloaded PDF — expect it to be reported as damaged/corrupted
  5. Repeat with a small one-page PDF — expect it to open correctly
  6. Compare uploaded and downloaded file sizes and checksums to identify truncation or byte corruption

## Environment
Production environment. Affects multiple customers. No specific browser or OS constraints reported — appears to be server-side. Standard file attachment workflow via the task attach button.

## Severity: high

## Impact
Multiple customers are unable to share PDF documents through TaskFlow, blocking team workflows that depend on PDF attachments. The feature is described as heavily used.

## Recommended Fix
1. Check deployment/change logs from approximately one week ago for changes to the file upload pipeline, storage configuration, or related infrastructure. 2. Compare binary content of an uploaded large PDF (server/storage side) against the original to identify where corruption occurs (truncation, encoding, byte substitution). 3. Check for chunked transfer encoding issues, buffer size limits, or content-type handling changes. 4. If a recent deployment is identified, check its diff for changes to multipart upload handling, streaming logic, or storage client libraries.

## Proposed Test Case
Upload PDFs of varying sizes (100KB, 1MB, 5MB, 20MB) and verify byte-for-byte integrity by comparing SHA-256 checksums of the original file against the downloaded file. The test should also cover at least one non-PDF file type (e.g., PNG image) to confirm whether the issue is format-specific or general.

## Information Gaps
- Exact file size threshold where corruption begins (developer can determine via testing)
- Whether non-PDF file types are also affected (reporter was unsure; one anecdotal report of a 'weird' image)
- Exact date the regression was introduced (reporter estimates ~1 week; deployment logs will be more precise)
- Whether corruption is truncation, byte-level mangling, or encoding-related (binary comparison will reveal this)
