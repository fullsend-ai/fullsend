# Triage Summary

**Title:** PDF attachments corrupted during upload via web app — size-dependent, regression ~1 week ago

## Problem
PDF files uploaded to tasks through the web app (drag-and-drop or attach button) are corrupted and cannot be opened. Adobe Reader reports the file is 'damaged or wasn't decoded correctly.' The issue disproportionately affects larger files (2-5MB business documents like contracts and reports) while smaller PDFs may still work. Multiple customers are affected. The reporter also noted some images may appear 'a little off,' suggesting the corruption may not be PDF-specific.

## Root Cause Hypothesis
A backend change deployed approximately one week ago likely altered how file uploads are processed — most probably in chunked upload handling, multipart form parsing, or binary encoding for files above a certain size threshold. The size-dependent nature of the corruption (small files OK, 2-5MB files broken) points to a boundary condition in upload chunking or a Content-Transfer-Encoding issue where binary data is being treated as text (e.g., UTF-8 transcoding stripping bytes).

## Reproduction Steps
  1. Prepare a valid PDF file in the 2-5MB range (e.g., a multi-page business document)
  2. Open the TaskFlow web app and navigate to any task
  3. Upload the PDF using either drag-and-drop or the attachment button
  4. Download the uploaded PDF
  5. Attempt to open the downloaded PDF — expect it to be reported as damaged
  6. Repeat with a small PDF (<500KB) — expect it to work correctly
  7. Compare file sizes and binary content of original vs downloaded to identify where corruption occurs (truncation, encoding, byte substitution)

## Environment
TaskFlow web application (browser-based). No API usage involved. Reporter did not specify browser. Issue affects multiple customers uploading via standard web UI. No changes on the customer side — regression is server-side.

## Severity: high

## Impact
Multiple customers are unable to use file attachments for normal business workflows (contracts, reports). The file attachment feature is heavily used by affected customers and this is blocking their day-to-day operations. No known workaround.

## Recommended Fix
1. Check deployment history for changes made ~1 week ago to the file upload pipeline (upload endpoint, storage service, multipart handling, encoding). 2. Compare binary content of an original PDF vs the stored/downloaded version to identify the corruption pattern (truncation, encoding damage, chunk reassembly error). 3. Look for size-dependent code paths — chunked upload thresholds, streaming vs buffered processing, or Content-Type/Transfer-Encoding handling that differs by file size. 4. Check if the issue also reproduces with other binary file types (images, .docx) to determine if this is PDF-specific or a general binary upload issue.

## Proposed Test Case
Upload a 3MB PDF via the web app, download it, and assert byte-for-byte equality with the original. Additionally, test at boundary sizes (500KB, 1MB, 2MB, 5MB, 10MB) to identify the exact threshold where corruption begins. Include a regression test that uploads and round-trips binary files of various types and sizes.

## Information Gaps
- Exact corruption pattern unknown — could be truncation, encoding damage, or chunk reassembly error (dev team should do binary comparison)
- Whether previously uploaded PDFs (before the regression) still download correctly — would confirm upload-side vs download/serving-side issue
- Whether other binary file types (images, Word docs) are definitively affected or just PDFs
- Exact browser and OS of affected customers
- Whether the issue reproduces across all browsers or is browser-specific
