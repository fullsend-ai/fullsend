# Triage Summary

**Title:** Large PDF attachments are corrupted after upload via task detail view

## Problem
PDF files uploaded through the task detail view upload button are corrupted and cannot be opened. The issue disproportionately affects larger files (contracts, reports) while smaller/simpler PDFs upload successfully. Multiple users on the same team are affected. The reporter states this is a regression — it was working previously.

## Root Cause Hypothesis
A recent change (at or before v2.3.1) likely introduced a bug in how large file uploads are handled — possible causes include incorrect chunked upload reassembly, a file size limit silently truncating the upload, a timeout during upload of larger files, or a content-type/encoding issue in the upload pipeline that only manifests above a certain file size.

## Reproduction Steps
  1. Log into TaskFlow 2.3.1
  2. Navigate to any task's detail view
  3. Click the upload button and select a larger PDF (e.g., a multi-page contract or report, likely >5-10MB)
  4. Wait for upload to complete
  5. Attempt to open/download the uploaded PDF
  6. Observe that the PDF viewer reports the file as damaged or corrupted
  7. Repeat with a small, simple PDF (e.g., one-page text document) and observe it works correctly

## Environment
TaskFlow 2.3.1, Chrome (likely), multiple users affected across one team

## Severity: high

## Impact
Multiple customers are unable to attach larger PDF documents (contracts, reports) to tasks, directly impacting daily workflow for teams that rely heavily on file attachments.

## Recommended Fix
1. Check git history for recent changes to the file upload pipeline, attachment storage, or related middleware (especially anything touching chunked uploads, streaming, or size limits). 2. Compare the uploaded file's byte size and checksum against the original to determine if truncation or corruption is occurring during upload vs. storage vs. retrieval. 3. Test uploads at various file sizes to identify the threshold where corruption begins. 4. Check server-side logs for any errors or warnings during large file uploads (e.g., request body size limits, proxy timeouts).

## Proposed Test Case
Upload PDFs at multiple sizes (1MB, 5MB, 10MB, 25MB, 50MB) and verify that each downloaded file has an identical checksum to the original. Add a regression test that uploads a file above the identified corruption threshold and asserts byte-for-byte integrity.

## Information Gaps
- Exact file size threshold where corruption begins
- Whether the corruption is truncation, encoding mangling, or something else (comparing file sizes of original vs. uploaded would clarify)
- Exact browsers and OS versions across affected team members
- Whether the issue reproduces with non-PDF large files (images, documents)
