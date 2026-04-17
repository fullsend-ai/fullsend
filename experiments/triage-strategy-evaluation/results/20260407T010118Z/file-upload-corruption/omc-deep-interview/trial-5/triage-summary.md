# Triage Summary

**Title:** PDF attachments corrupted after upload — downloaded file is larger than original (likely encoding issue)

## Problem
When users upload PDF files via the task attachment button, the files become corrupted and cannot be opened. The downloaded file is larger than the original, indicating the file content is being transformed (not truncated). The issue began approximately 1–2 weeks ago and primarily affects larger PDFs (~2.4MB+). Smaller PDFs may be unaffected.

## Root Cause Hypothesis
A recent change in the upload or download pipeline is applying an encoding transformation to binary file data — most likely base64-encoding the file content without properly decoding it on retrieval (or double-encoding it). This would explain why the output file is larger than the input (~33% size increase is characteristic of base64) and why the file is structurally corrupted. Smaller files may appear to work if they fall below a chunked-transfer threshold or a different code path.

## Reproduction Steps
  1. Prepare a PDF file approximately 2–3MB in size
  2. Open a task in TaskFlow and click the attachment button
  3. Upload the PDF file
  4. Download the attached file from the task
  5. Compare the file sizes — the download should be noticeably larger than the original
  6. Attempt to open the downloaded PDF — it should report as damaged/corrupted

## Environment
Not browser-specific as far as known. Affects multiple customers. Issue appeared within the last 1–2 weeks, suggesting a recent deployment introduced the regression.

## Severity: high

## Impact
Multiple customers are affected. Teams that rely on PDF attachments (e.g., sharing monthly reports) have no workaround and cannot use the attachment feature for their core workflow.

## Recommended Fix
Review git history from the last 1–2 weeks for changes to the file upload/download pipeline, particularly: (1) any change to Content-Type or Content-Transfer-Encoding headers, (2) middleware that processes request/response bodies (e.g., a new body-parser or serialization layer that base64-encodes binary data), (3) changes to the storage layer (S3 upload params, database blob handling). Compare the raw bytes of an uploaded file in storage against the original to confirm where the transformation occurs. If base64-encoding is confirmed, ensure binary files are stored and served as raw binary with appropriate Content-Type headers.

## Proposed Test Case
Upload a PDF of known size and checksum, download it, and assert that the downloaded file has the same size and checksum as the original. Include test cases at multiple file sizes (100KB, 1MB, 5MB) to identify any size-dependent threshold.

## Information Gaps
- Exact size threshold at which corruption begins (reporter unsure if smaller PDFs truly work or just haven't been noticed)
- Whether non-PDF binary file types (images, Word docs) are also affected
- Specific browser and OS used by affected customers
- Exact deployment or commit that introduced the regression
