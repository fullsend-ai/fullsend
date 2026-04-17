# Triage Summary

**Title:** Large PDF attachments uploaded via task detail view are corrupted/unreadable on download

## Problem
PDF files uploaded through the task detail view paperclip icon are completely corrupted when downloaded. The files cannot be opened — PDF viewers report them as damaged, and forcing them open shows garbled text. The issue is size-dependent: small single-page PDFs work fine, but larger multi-page documents (customer contracts, reports) are consistently broken. Multiple customers are affected. The issue began approximately one week ago with no changes on the client side.

## Root Cause Hypothesis
A recent server-side change (likely deployed ~1 week ago) is corrupting larger file uploads. The fact that files are fully garbled (not truncated) and the issue is size-dependent suggests either: (1) a chunked upload/transfer encoding issue where reassembly is broken for multi-chunk files, (2) a content-type or binary encoding error applied during upload processing that only manifests above a single-chunk threshold, or (3) a middleware or proxy change (e.g., request body size limit, compression, or encoding transformation) that mangles payloads above a certain size.

## Reproduction Steps
  1. Prepare two PDFs: one small single-page file (<1 MB) and one larger multi-page file (e.g., 5+ MB contract)
  2. Navigate to any task's detail view in TaskFlow
  3. Click the paperclip icon to upload the small PDF
  4. Download the uploaded small PDF and verify it opens correctly (expected: works fine)
  5. Repeat upload with the larger multi-page PDF
  6. Download the uploaded large PDF and attempt to open it (expected: corrupted/damaged error)
  7. Examine the downloaded file in a hex editor — compare headers and size to the original to determine if the file is garbled, truncated, or has incorrect encoding

## Environment
TaskFlow web application, task detail view file attachment feature. Browser and OS not specific to the issue (no client-side changes). Issue is server-side and affects multiple customers.

## Severity: high

## Impact
Multiple customers are unable to upload and share large PDF documents (contracts, reports) that are core to their workflow. Teams are blocked on deadline-sensitive work. The file attachment feature is heavily used, making this a broad-impact regression.

## Recommended Fix
1. Check deployment history for changes made ~1 week ago to the file upload pipeline (upload endpoint, storage service, middleware, reverse proxy config). 2. Compare the raw bytes of an uploaded large file in storage against the original — determine whether corruption happens during upload, storage, or download. 3. Investigate chunked transfer encoding or multipart upload handling for files above the single-chunk threshold. 4. Check for recently added middleware that might transform request/response bodies (compression, encoding, content-type normalization). 5. Verify that the upload handler preserves binary content-type (application/octet-stream or application/pdf) and doesn't apply text encoding transformations.

## Proposed Test Case
Upload PDFs of varying sizes (1 MB, 5 MB, 10 MB, 25 MB) through the task detail attachment feature. Download each and perform a byte-for-byte comparison against the original using checksums (SHA-256). All files should match exactly. Additionally, add an automated integration test that uploads a binary file above the chunking threshold and verifies the downloaded file's checksum matches the original.

## Information Gaps
- Exact file size threshold where corruption begins (reporter couldn't specify, but developers can determine via testing)
- Whether the corruption also affects non-PDF binary files (images, Word docs) or is PDF-specific
- Exact deployment or change that occurred ~1 week ago that may have introduced the regression
