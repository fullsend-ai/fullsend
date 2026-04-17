# Triage Summary

**Title:** File uploads corrupted for larger files (~3MB+) since recent deployment — PDFs and possibly images affected

## Problem
Files uploaded to task attachments are corrupted when downloaded. PDF viewers report files as damaged. The issue primarily affects larger files (3-4MB range), while smaller files appear to work correctly. At least one image was also reported as looking 'weird' after download, suggesting the corruption is not PDF-specific. The downloaded file size appears roughly preserved, pointing to content corruption rather than truncation.

## Root Cause Hypothesis
A deployment or infrastructure change approximately one week ago introduced a corruption in the file upload or download pipeline that affects files above a certain size threshold. Since file sizes appear preserved but content is corrupted, likely causes include: (1) an encoding or content-type transformation applied incorrectly to binary data (e.g., UTF-8 re-encoding of binary streams), (2) a change in multipart upload handling or chunked transfer that corrupts data at chunk boundaries, or (3) a middleware or proxy change (e.g., response compression, content transformation) that mangles binary content above a buffering threshold.

## Reproduction Steps
  1. Upload a PDF file in the 3-4MB range to a task attachment via the browser UI
  2. Download the same file from the task via the browser UI
  3. Attempt to open the downloaded PDF — it should appear corrupted/damaged
  4. Repeat with a small PDF (<1MB) — this should work correctly
  5. Repeat with a large non-PDF binary file (e.g., a 3-4MB image) to confirm cross-format corruption

## Environment
Browser-based uploads and downloads. No specific browser identified. Multiple customers affected across the team. Production environment.

## Severity: high

## Impact
Multiple customers are unable to use file attachments for business-critical documents (contracts, reports). The feature is heavily used and has been broken for approximately one week. Workaround is limited to using only small files.

## Recommended Fix
1. Review all deployments and infrastructure changes from approximately one week ago (late March / early April 2026). 2. Compare the binary content of a stored file against its original to identify where corruption occurs (upload path vs. storage vs. download path). 3. Check for changes in: upload middleware, content-type handling, response streaming/compression, proxy or CDN configuration, any size-based thresholds (chunked uploads, multipart boundaries). 4. Specifically look for any encoding transformation being applied to binary streams (e.g., text encoding on binary data). 5. Test the specific file 'ServiceAgreement_2026.pdf' if still available in storage.

## Proposed Test Case
Upload binary files of varying sizes (500KB, 1MB, 2MB, 4MB, 10MB) across multiple formats (PDF, PNG, DOCX). Download each and perform a byte-for-byte comparison (SHA-256 hash) between the original and downloaded file. The test passes when all hashes match. Additionally, add a regression test that uploads a known binary file through the full upload/download pipeline and asserts byte-level integrity.

## Information Gaps
- Exact size threshold where corruption begins (reporter only noted 'smaller ones seem fine')
- Specific browser(s) and OS in use (though likely not browser-specific given multiple customers affected)
- Whether the corruption occurs on upload (stored file is already corrupt) or on download (stored file is intact but served incorrectly) — needs server-side investigation
- Exact deployment or change from ~1 week ago that correlates with the onset
