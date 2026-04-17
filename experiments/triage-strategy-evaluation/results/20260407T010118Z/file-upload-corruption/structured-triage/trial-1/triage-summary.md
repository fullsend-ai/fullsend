# Triage Summary

**Title:** PDF attachments corrupted on download — intermittent, recent regression (v2.3.1)

## Problem
PDF files uploaded via the attach button in task view are sometimes corrupted when downloaded. Adobe Acrobat reports 'The file is damaged and could not be repaired.' The issue is intermittent — some PDFs upload and download fine, others do not. Multiple users across different browser/OS setups are affected, indicating a server-side issue. The reporter states this is a recent regression; uploads were working correctly until approximately 1-2 weeks ago.

## Root Cause Hypothesis
A recent server-side change (within the last 1-2 weeks) is corrupting certain PDF uploads or downloads. Likely candidates: (1) a change in the file upload pipeline that incorrectly processes binary data for some files (e.g., encoding conversion, chunking, or content-type handling), (2) a storage backend change that truncates or modifies files above a certain size or with certain characteristics, or (3) a CDN/proxy layer change that interferes with binary content delivery.

## Reproduction Steps
  1. Open a task in TaskFlow 2.3.1
  2. Click the attach button in the task view
  3. Upload a PDF file (try several — the issue is intermittent)
  4. Download the uploaded PDF
  5. Attempt to open the downloaded file in Adobe Acrobat
  6. Observe error: 'The file is damaged and could not be repaired'

## Environment
TaskFlow 2.3.1, Chrome (latest) on Windows 10. Reproduced by multiple users across different browser/OS configurations.

## Severity: high

## Impact
Multiple customers are affected. Teams that rely heavily on PDF attachments (reports, contracts) are unable to use the file attachment feature reliably, disrupting core workflows.

## Recommended Fix
1. Review all server-side changes to the file upload/download pipeline in the last two weeks. 2. Compare byte-for-byte a corrupted downloaded PDF against its original to identify where corruption occurs (truncation, encoding, byte substitution). 3. Check whether corruption happens at upload (stored file is already bad) or download (stored file is fine but served incorrectly) by examining files directly in the storage backend. 4. Test with PDFs of varying sizes and characteristics to identify the triggering pattern.

## Proposed Test Case
Upload a set of diverse PDF files (varying sizes, single-page vs multi-page, text-only vs image-heavy) via the attach button, download each one, and verify the downloaded file matches the original byte-for-byte (SHA-256 comparison). This test should run against both the current version and the last known-good deployment to confirm the regression.

## Information Gaps
- Exact characteristics that distinguish corrupted PDFs from ones that upload successfully (file size, PDF version, content type)
- Precise date the issue started occurring
- Whether the stored files in the backend are already corrupted or only corrupted on download
