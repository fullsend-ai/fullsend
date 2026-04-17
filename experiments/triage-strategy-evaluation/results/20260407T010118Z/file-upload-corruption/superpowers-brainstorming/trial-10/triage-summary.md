# Triage Summary

**Title:** PDF attachments corrupted after upload — content mangled despite correct file size (regression ~1 week)

## Problem
PDF files uploaded as task attachments are silently corrupted. When downloaded back, the file size is approximately correct but the PDF viewer reports the file as damaged. Simple/small PDFs survive the round-trip; more complex ones do not. This is a regression that appeared roughly one week ago and is affecting multiple customers.

## Root Cause Hypothesis
A recent change in the upload or storage pipeline is applying a content transformation to binary data — most likely a character encoding conversion (e.g., treating the binary stream as UTF-8 text) or a middleware/proxy layer re-encoding the body. This would explain why file sizes are roughly preserved (byte-for-byte substitution of invalid sequences) while the binary content is destroyed. Simple PDFs with mostly ASCII-range bytes survive; complex PDFs with arbitrary binary content in embedded fonts, images, or streams do not.

## Reproduction Steps
  1. Upload a multi-page PDF with embedded images or fonts (>1 page, ~3MB) as a task attachment
  2. Download the attachment back from the task
  3. Attempt to open the downloaded PDF — it will report as corrupted
  4. Compare the original and downloaded file sizes — they should be approximately equal
  5. Repeat with a simple one-page text-only PDF — this one should open correctly

## Environment
Production environment; not browser-specific based on report (multiple customers affected)

## Severity: high

## Impact
Multiple customers are unable to use the file attachment feature for non-trivial PDFs. This is a core workflow for affected users and is customer-facing, causing reputational damage.

## Recommended Fix
Review all commits to the upload/attachment pipeline from the past 1–2 weeks. Look specifically for: (1) changes to content-type handling or stream encoding, (2) new middleware or proxy layers in the upload path, (3) changes to the storage client configuration (e.g., a base64 or encoding flag), (4) any text/binary mode changes in file I/O. Do a binary diff between an original PDF and its round-tripped version to confirm the corruption pattern — UTF-8 mangling will show characteristic 0xEF 0xBF 0xBD (U+FFFD replacement character) sequences.

## Proposed Test Case
Upload a PDF containing embedded binary streams (fonts, images) and assert that the downloaded file is byte-identical to the original (SHA-256 hash comparison). Include PDFs of varying complexity: text-only single page, multi-page with images, and a PDF with embedded fonts.

## Information Gaps
- Whether non-PDF binary attachments (e.g., images, ZIP files) are also affected — would confirm this is a general binary handling issue vs. PDF-specific
- The exact date or deployment that introduced the regression
- Whether the corruption is consistent (same file always corrupts the same way) or non-deterministic
