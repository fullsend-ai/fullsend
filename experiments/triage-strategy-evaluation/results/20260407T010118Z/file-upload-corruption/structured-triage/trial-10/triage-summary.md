# Triage Summary

**Title:** PDF attachments corrupted on download after upload via task page (v2.3.1)

## Problem
PDF files uploaded through the task page attachment button appear to upload successfully (no client-side errors), but when subsequently downloaded, the files are corrupted and cannot be opened. Adobe Reader reports the file is damaged. The issue affects multiple users across the reporter's team and is browser-independent.

## Root Cause Hypothesis
The corruption occurs server-side during upload processing or storage, not during the browser upload itself (no client errors, reproduces across Chrome and Firefox). Likely candidates: (1) a encoding/transformation issue in the upload pipeline (e.g., binary file being processed as text, incorrect Content-Type handling, or a middleware stripping/modifying binary data), (2) a storage layer issue truncating or corrupting the file bytes, or (3) a regression introduced before v2.3.1 in the file handling code. The reporter's observation that smaller files may be unaffected suggests possible chunked upload or streaming issues with larger payloads.

## Reproduction Steps
  1. Open a task in TaskFlow v2.3.1
  2. Click the attachment button on the task page
  3. Select a PDF file (try one >1 MB) via the file picker
  4. Upload completes with no visible errors
  5. Download the uploaded PDF from the task
  6. Attempt to open the downloaded PDF — expect Adobe Reader to report the file is damaged

## Environment
TaskFlow v2.3.1, Windows 10, Chrome and Firefox (browser-independent), standard task page file attachment feature

## Severity: high

## Impact
Multiple users on the same team are unable to use PDF attachments, which is a core workflow for them. File attachments are effectively broken for their primary use case (PDFs). The reporter indicates this is a recent regression.

## Recommended Fix
Compare the byte content of a PDF before upload and after download to identify where corruption occurs (truncation, encoding change, byte insertion). Check the upload endpoint for any middleware that processes request bodies as text rather than binary streams. Inspect Content-Type and transfer-encoding headers. Review recent changes to the file upload/storage/download pipeline around the v2.3.1 release for regressions. Test with varying file sizes to confirm the size-correlation hypothesis.

## Proposed Test Case
Upload a PDF of known size and checksum via the task page attachment button, then download it and verify the checksum matches. Repeat with multiple file sizes (e.g., 100 KB, 1 MB, 10 MB) to identify a corruption threshold. Also test with non-PDF binary files (e.g., DOCX, PNG) to determine if the issue is PDF-specific or affects all binary uploads.

## Information Gaps
- Exact file size threshold at which corruption begins (reporter unsure)
- Whether non-PDF file types are also affected (reporter mostly uses PDFs, uncertain about images)
- Exact date or version when the issue started (reporter says 'recently' but no specific version or date)
- Server-side logs or error output during upload/download (not available from reporter)
