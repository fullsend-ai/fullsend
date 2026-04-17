# Triage Summary

**Title:** PDF attachments corrupted on upload — regression introduced ~1 week ago, affects larger files

## Problem
PDF files attached to tasks are being corrupted during upload or storage. Customers cannot open the resulting files — PDF viewers report them as damaged. The issue disproportionately affects larger files (typical business documents like contracts and reports), while smaller PDFs may still work. This is a regression: the feature was working correctly until approximately one week ago, when it suddenly broke.

## Root Cause Hypothesis
A deployment or configuration change approximately one week ago introduced a bug in the file upload pipeline that corrupts larger files. Likely candidates: a change to chunked upload handling, multipart form parsing, stream processing, or storage write logic that truncates or mangles file data above a certain size threshold. Binary file encoding issues (e.g., inadvertent text-mode processing of binary data) are also possible.

## Reproduction Steps
  1. Upload a PDF file of ~5 MB or larger to a task as an attachment
  2. Download the uploaded attachment
  3. Attempt to open the downloaded PDF — it should report as corrupted or damaged
  4. Repeat with a very small PDF (~100 KB) to confirm smaller files are unaffected
  5. Compare the byte content of the original and uploaded files to identify where corruption occurs (truncation, encoding, or byte mangling)

## Environment
Server-side issue affecting all users. Not browser- or OS-specific. Production environment, occurring since approximately one week ago.

## Severity: critical

## Impact
Multiple customers are unable to access uploaded PDF attachments, blocking teams that rely on the file attachment feature for daily workflows. The reporter describes their team as 'stuck' and the issue as ongoing for about a week.

## Recommended Fix
1. Review all deployments and configuration changes from approximately one week ago, focusing on file upload, attachment processing, and storage subsystems. 2. Compare uploaded file bytes against source files to determine the nature of the corruption (truncation, encoding, byte substitution). 3. Check for changes to: multipart upload handling, stream/buffer processing, file size limits, storage backend configuration, or any middleware that touches file data. 4. If a chunked upload or streaming change is identified, verify that binary data is not being processed as text (e.g., UTF-8 encoding applied to binary streams).

## Proposed Test Case
Upload PDFs of varying sizes (100 KB, 1 MB, 5 MB, 10 MB, 25 MB) and verify that each downloaded attachment is byte-identical to the original. Include a regression test that uploads a binary file, downloads it, and asserts a checksum match.

## Information Gaps
- Exact file size threshold where corruption begins
- Whether non-PDF binary files (images, DOCX, ZIP) are also affected or only PDFs
- The specific deployment or commit that introduced the regression
