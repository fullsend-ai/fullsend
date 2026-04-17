# Triage Summary

**Title:** File upload corrupts large PDF attachments (size-dependent regression, ~1 week old)

## Problem
PDF files attached to tasks are being corrupted during upload, rendering them unopenable. The issue is size-dependent — smaller PDFs appear to work while larger ones are consistently broken. Multiple customers are affected. Other file types (images, spreadsheets) have not been reported as broken but have not been rigorously checked. The issue started approximately one week ago, indicating a regression.

## Root Cause Hypothesis
A change deployed ~1 week ago likely introduced a bug in the file upload pipeline that corrupts binary data above a certain size threshold. Common causes for this pattern: (1) a chunked upload change that incorrectly reassembles chunks, (2) a text/encoding transformation (e.g., UTF-8 conversion) being applied to binary streams, (3) a new file size limit or compression step that truncates or re-encodes large files, or (4) a content-type handling change that misidentifies binary files.

## Reproduction Steps
  1. Upload a small PDF (<1 MB) to a task and verify it opens correctly
  2. Upload a larger PDF (>5 MB) to a task
  3. Download the uploaded large PDF and attempt to open it
  4. Compare the byte size and checksum of the original vs. downloaded file to confirm data corruption
  5. Repeat with a large non-PDF binary file (e.g., a large image) to determine if the issue is PDF-specific or affects all large binary uploads

## Environment
Production environment; affects multiple customers. No specific browser/OS constraints reported — likely server-side.

## Severity: high

## Impact
Multiple customers are unable to use PDF attachments, which is a core part of their workflow. Data loss is occurring (uploaded files are unrecoverable in their corrupted form).

## Recommended Fix
Review all commits to the file upload/attachment pipeline from the past 7-10 days. Look specifically for changes to: chunked upload handling, binary stream processing, content-type detection, file size limits, compression, or encoding transformations. Compare the byte stream of an uploaded large PDF against its original to identify where corruption occurs (truncation, encoding mangling, or chunk reassembly errors). A binary diff between the original and corrupted file will quickly reveal the corruption pattern.

## Proposed Test Case
Upload PDFs of varying sizes (100KB, 1MB, 5MB, 20MB) and verify that the downloaded files are byte-identical to the originals (matching checksums). Include at least one non-PDF binary file of similar size to confirm the fix isn't format-specific.

## Information Gaps
- Exact file size threshold where corruption begins (developer can determine via testing)
- Whether large non-PDF binary files are also affected (developer can test during investigation)
- The specific nature of the corruption — truncation vs. encoding mangling vs. chunk errors (binary diff will reveal)
