# Triage Summary

**Title:** PDF attachments silently corrupted during upload/download — size preserved, content damaged (regression, ~1 week)

## Problem
PDF files uploaded to task attachments are being corrupted. When customers download the files, PDF viewers report them as damaged. The file size is preserved (a 2.4MB original downloads as ~2.4MB), ruling out truncation. The issue disproportionately affects larger, multi-page PDFs while smaller single-page PDFs tend to survive intact. Multiple customers are affected. This is a regression that began approximately one week ago; the feature was previously working correctly.

## Root Cause Hypothesis
Size-preserved corruption strongly suggests a binary-to-text encoding issue introduced in a recent change. Most likely: a component in the upload or download pipeline is now treating the binary PDF stream as text and applying a character encoding transformation (e.g., UTF-8 sanitization, BOM insertion, or line-ending normalization). This would corrupt multi-byte sequences in the binary data while leaving simpler/smaller files less visibly damaged. Alternatively, a new middleware layer (content filter, virus scanner, or CDN edge processing) may be modifying the binary stream. A chunked upload reassembly bug where chunks are reordered is also possible but less likely given that some files work fine regardless of the corruption.

## Reproduction Steps
  1. Prepare a multi-page PDF file (>1MB, e.g., a report or scanned document)
  2. Upload the PDF as a task attachment in TaskFlow
  3. Download the attachment from TaskFlow
  4. Attempt to open the downloaded file — it should fail with a 'damaged/corrupted' error
  5. Compare the original and downloaded file sizes (expect them to match)
  6. Run a hex diff between original and downloaded copies to identify the corruption pattern

## Environment
Affects multiple customers across unknown browsers/OS. The feature is used heavily by support teams. Regression window is approximately 1 week prior to report (late March / early April 2026).

## Severity: high

## Impact
Multiple customers' daily workflows are blocked. Support teams that rely on PDF attachments are forced to use email as a workaround, degrading productivity and creating process fragmentation.

## Recommended Fix
1. Identify all deployments/config changes to the file upload/download pipeline from the past 1-2 weeks. 2. Hex-diff an original PDF against its downloaded copy to characterize the corruption pattern (look for byte substitutions consistent with encoding conversion, line-ending changes, or chunk boundary artifacts). 3. Inspect the upload and download code paths for any text-mode stream handling, encoding transforms, or new middleware. 4. Check for changes to storage layer configuration, CDN settings, or reverse proxy rules that might alter binary content. 5. Verify Content-Type headers are set to application/octet-stream or application/pdf (not text/*) throughout the pipeline.

## Proposed Test Case
Upload PDFs of varying sizes (100KB, 1MB, 5MB, 10MB) and binary complexity. After download, perform a byte-for-byte comparison (e.g., sha256sum) between original and downloaded files. All checksums must match. Include PDFs with embedded images, forms, and multi-byte unicode text to cover encoding edge cases.

## Information Gaps
- Exact date the regression was introduced (reporter said 'about a week ago' — server-side deploy logs will pinpoint this)
- Whether non-PDF binary files (images, DOCX, ZIP) are also affected (engineering can test this to confirm whether the bug is PDF-specific or affects all binary uploads)
- Exact corruption pattern visible in a hex diff (engineering will obtain this during investigation)
- Which specific browsers/clients are in use (unlikely to matter if the corruption is server-side, as suggested by multiple customers being affected)
