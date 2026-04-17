# Triage Summary

**Title:** PDF attachments corrupted on upload — content encoding regression (~1 week ago)

## Problem
PDF files uploaded as task attachments are rendered unopenable/damaged by PDF viewers. The issue affects larger PDFs while smaller ones appear to work. File sizes between the original and uploaded copy are approximately the same, ruling out truncation. This is a regression that started roughly one week ago.

## Root Cause Hypothesis
A recent change to the file upload pipeline is corrupting binary content during transfer or storage — most likely a character encoding transformation (e.g., binary stream processed as UTF-8 text), an incorrect Content-Transfer-Encoding, or a middleware/filter that re-encodes or transforms the upload body. The file size stays roughly the same because the data isn't being truncated, but byte sequences that are invalid in the assumed encoding are being replaced or mangled. Smaller files are less likely to contain problematic byte sequences, which is why they appear unaffected.

## Reproduction Steps
  1. Upload a PDF larger than ~1 MB to a task as an attachment
  2. Download the uploaded attachment
  3. Attempt to open the downloaded file in a PDF viewer
  4. Observe that the viewer reports the file as damaged or corrupted
  5. Compare the file size of the original and downloaded copy — they should be approximately equal, confirming the data is not truncated but corrupted

## Environment
TaskFlow file attachment feature; affects multiple customers; no specific browser/OS indicated (likely server-side issue given widespread reports)

## Severity: high

## Impact
Multiple customers are unable to use PDF attachments, a heavily-used feature. Uploaded files are silently corrupted with no error feedback, causing data loss and eroding customer trust.

## Recommended Fix
Review all changes to the file upload/attachment pipeline from the past 1–2 weeks. Specifically investigate: (1) any change to how uploaded file streams are read or written — look for text-mode I/O where binary-mode is required, (2) middleware that may process request bodies as text (e.g., body-parser with text/charset handling applied to multipart uploads), (3) changes to storage layer encoding (base64 encode/decode, Content-Type headers), (4) any dependency updates to upload-handling libraries. Compare a hex dump of an original PDF against the stored/downloaded version to identify the exact corruption pattern (e.g., 0xFFFD replacement characters indicating UTF-8 re-encoding of binary data).

## Proposed Test Case
Upload PDFs of varying sizes (100 KB, 1 MB, 10 MB) and perform a byte-for-byte comparison (e.g., sha256sum) between the original and the downloaded attachment. All checksums must match exactly. Additionally, add a regression test that uploads a known binary file and asserts the downloaded content is byte-identical.

## Information Gaps
- Exact file size threshold at which corruption begins (dev can determine via testing)
- Whether non-PDF binary attachments (images, zips) are also affected (same root cause likely applies)
- The specific commit or deployment that introduced the regression (dev team can correlate with ~1 week ago timeline)
