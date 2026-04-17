# Triage Summary

**Title:** Large file attachments silently corrupted during upload/download (size-dependent truncation)

## Problem
PDF attachments uploaded to tasks via the attach button appear to upload successfully (no errors), but are corrupted and unopenable when later downloaded. The issue is size-dependent: small one-page PDFs work fine, while larger multi-page reports and contracts are broken. The reporter also noted images may have looked 'a little off,' suggesting the problem is not PDF-specific. The issue began approximately 1-2 weeks ago; attachments worked correctly before that.

## Root Cause Hypothesis
A recent change (likely a deployment, infrastructure update, or configuration change in the past 1-2 weeks) introduced a file size limit or broke chunked/multipart upload handling, causing large files to be silently truncated or corrupted during upload, storage, or download. The absence of any client-side error suggests the truncation happens server-side after the upload is accepted.

## Reproduction Steps
  1. Create or open a task in TaskFlow
  2. Click the attach button and upload a small PDF (< 1 MB, single page) — confirm it downloads correctly
  3. Upload a larger PDF (multi-page, several MB) using the same method
  4. Download the larger PDF and attempt to open it — expect it to be reported as damaged/corrupted
  5. Compare the file size of the uploaded original to the downloaded file to confirm truncation

## Environment
Affects multiple users (team of 15). No specific browser, OS, or network details provided, but the issue is consistent across the team, suggesting a server-side problem rather than a client-specific one.

## Severity: high

## Impact
A team of 15 is blocked from using file attachments, which is central to their workflow. The issue has persisted for 1-2 weeks. Any customer relying on attachments for files above the threshold is likely affected.

## Recommended Fix
1. Check deployment/change history for the past 2 weeks for anything touching file upload, storage, or download paths (e.g., multipart upload config, proxy body-size limits, storage gateway changes). 2. Compare stored file sizes in the backend to known originals to confirm truncation vs. encoding corruption. 3. Check for recently introduced file size limits (e.g., nginx client_max_body_size, API gateway payload limits, cloud storage chunking config). 4. If chunked uploads are used, verify chunk reassembly logic. 5. Test with various file sizes to identify the exact threshold where corruption begins.

## Proposed Test Case
Upload files at various sizes (100 KB, 1 MB, 5 MB, 10 MB, 25 MB) across multiple file types (PDF, PNG, DOCX). Download each and verify byte-for-byte integrity against the original using checksums. The test should assert that downloaded file hash matches uploaded file hash for all sizes up to the documented maximum.

## Information Gaps
- Exact file size threshold where corruption begins (determinable from server-side testing)
- Whether the corruption is truncation vs. encoding damage (determinable by comparing stored file sizes)
- Which specific deployment or config change caused the regression (determinable from change history)
- Whether the issue affects all file types equally or varies (partially answered — PDFs confirmed, images possibly affected)
