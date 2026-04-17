# Triage Summary

**Title:** Large PDF uploads are silently corrupted (content garbled, not truncated) — regression from ~1 week ago

## Problem
PDF files uploaded to tasks are corrupted for multiple customers. The corruption affects larger multi-page PDFs (reports, contracts) while single-page PDFs upload correctly. The uploaded file retains its original size but the content is garbled, producing 'invalid format' / 'not a valid PDF' errors in viewers. The corruption is deterministic — the same file fails every time.

## Root Cause Hypothesis
A change deployed approximately one week ago introduced a bug in how larger file uploads are processed. Since the file size is preserved but content is garbled (not truncated), this is likely a character encoding or binary data handling issue — for example, a middleware or proxy treating the binary upload stream as text (applying UTF-8 encoding/decoding to binary data), or a base64/multipart encoding regression that activates only on chunked uploads (i.e., files above the chunking threshold). The size-dependent behavior strongly suggests a different code path is used above a certain file size.

## Reproduction Steps
  1. Upload a small single-page PDF (e.g., <1MB) to a task — confirm it uploads and opens correctly
  2. Upload a larger multi-page PDF (e.g., a 5+ page report or contract) to a task
  3. Download the uploaded file and attempt to open it
  4. Observe that the viewer reports 'invalid format' or 'not a valid PDF'
  5. Compare the uploaded and downloaded file sizes — they should be approximately equal
  6. Run a binary diff or hex dump to identify how the content was mangled (look for encoding artifacts like UTF-8 replacement characters 0xEFBFBD, or base64 double-encoding)

## Environment
Production environment, multiple customers affected, file attachment feature. No specific browser/OS constraints reported — appears to be a server-side issue.

## Severity: high

## Impact
Multiple customers are unable to use file attachments for larger PDFs, which is a core workflow. The feature has been broken for approximately one week. Customers are actively complaining. Small files still work, providing a partial workaround.

## Recommended Fix
1. Review all changes deployed ~1 week ago that touch the file upload pipeline, file storage, or any middleware/proxy in the upload path. 2. Check whether uploads above a certain size take a different code path (e.g., chunked/multipart uploads vs. single-request uploads). 3. Binary-diff a corrupted download against its original to identify the corruption pattern (encoding mangling, double-encoding, etc.). 4. Check if a reverse proxy, CDN, or API gateway was updated and is now modifying binary request bodies. 5. Test with other binary file types (images, DOCX) to determine if the issue is PDF-specific or affects all binary uploads above the threshold.

## Proposed Test Case
Upload PDFs of increasing size (500KB, 1MB, 2MB, 5MB, 10MB) and verify each downloaded file is byte-identical to the original. This identifies the exact size threshold. Additionally, perform a byte-level comparison of a corrupted file against its source to confirm the corruption pattern and regression-test the fix.

## Information Gaps
- Exact file size threshold where corruption begins
- Whether non-PDF binary files (images, DOCX, ZIP) are also affected
- Exact deployment or infrastructure change made ~1 week ago
- Specific browser and upload method used (drag-and-drop vs. file picker, though likely irrelevant given server-side symptoms)
