# Triage Summary

**Title:** PDF attachments corrupted after upload — regression affecting larger files via web browser

## Problem
PDF files uploaded as task attachments through the web browser are being corrupted. When downloaded, the PDF viewer reports the file as damaged and cannot open it. The issue disproportionately affects larger PDFs while smaller ones tend to work fine. Multiple customers are affected and this is blocking a core workflow.

## Root Cause Hypothesis
A change deployed approximately one week ago likely introduced a bug in the file upload or storage pipeline that corrupts larger files. Probable causes include: (1) a chunked upload change that truncates or misassembles chunks beyond a size threshold, (2) an encoding or content-type handling change that mangles binary data for larger payloads, or (3) a middleware/proxy change (e.g., request body size limit, compression) that silently corrupts uploads above a certain size.

## Reproduction Steps
  1. Upload a small PDF (under ~1 MB) as a task attachment via the web browser — verify it downloads and opens correctly
  2. Upload a larger PDF (5+ MB) as a task attachment via the web browser
  3. Download the uploaded file
  4. Attempt to open — expect PDF viewer to report corruption
  5. Compare file sizes of original and downloaded file to check for truncation
  6. Inspect file headers (e.g., `hexdump -C`) to identify where corruption begins

## Environment
Web browser upload path. No specific browser identified. Issue is approximately one week old, indicating a regression tied to a recent deployment.

## Severity: high

## Impact
Multiple customers are unable to use the file attachment feature for larger PDFs. This is described as a core part of their workflow, so it is actively blocking daily work for affected users.

## Recommended Fix
1. Review all commits/deployments from the past 7-10 days that touch the file upload pipeline, storage layer, or any middleware/proxy configuration (e.g., nginx body size limits, multipart handling, streaming/chunking logic). 2. Pull a corrupted file from storage and diff against a known-good upload to identify the corruption pattern (truncation, encoding, chunk boundary issue). 3. Check for any size-based thresholds in the upload path (multipart chunk size, max body size, buffering limits). 4. Verify the Content-Type header is preserved as application/octet-stream or application/pdf through the full upload chain.

## Proposed Test Case
Upload PDFs of varying sizes (100 KB, 1 MB, 5 MB, 10 MB, 25 MB) through the web browser, download each, and verify byte-for-byte integrity against the original using checksums (sha256sum). This test should be added as an integration test against the upload endpoint.

## Information Gaps
- Exact file size threshold where corruption begins — engineering team should determine this via systematic testing
- Nature of corruption (truncation vs. encoding vs. chunk reassembly) — requires server-side file inspection
- Whether the same file consistently corrupts on re-upload or if the corruption is intermittent
- Specific browser(s) affected — reporter did not specify; worth testing across Chrome, Firefox, Safari
- Whether the API or mobile upload paths are also affected or only the web browser path
