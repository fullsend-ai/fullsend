# Triage Summary

**Title:** Binary file corruption on upload via web UI — regression from ~1 week ago

## Problem
Files uploaded through the web UI (confirmed for PDFs, likely also images) are silently corrupted. Downloaded files are approximately the same size as the originals but cannot be opened — PDF viewers report the file as damaged/invalid. Multiple customers are affected. Smaller files may be unaffected, suggesting the corruption may be size-dependent or that the reporter hasn't tested small files carefully.

## Root Cause Hypothesis
A recent change (~1 week ago) to the upload pipeline is corrupting binary data in transit or at rest. The file size being preserved rules out truncation and points to a content transformation issue. Most likely candidates: (1) a middleware or proxy change that re-encodes the request body (e.g., treating binary as UTF-8 text, which destroys non-ASCII bytes while preserving approximate size), (2) a double base64 encoding/decoding mismatch, or (3) a compression layer that corrupts the stream. The fact that multiple binary formats are affected confirms this is in the general upload path, not PDF-specific processing.

## Reproduction Steps
  1. Upload a PDF file (>100KB to be safe) to a task via the web UI
  2. Download the same file from the task
  3. Attempt to open the downloaded file — it will be reported as damaged
  4. Compare the uploaded and downloaded file sizes (should be approximately equal)
  5. Optionally compare binary content with a hex diff to identify the corruption pattern

## Environment
Web UI upload path. No specific browser/OS dependency reported (likely server-side). Production environment, multiple customers affected.

## Severity: high

## Impact
Multiple customers cannot use the file attachment feature for PDFs or potentially any binary file type. Customers using TaskFlow for document exchange with their own clients are directly blocked. No known workaround.

## Recommended Fix
1. Review all deployments and config changes to the upload pipeline from the past 1-2 weeks. 2. Binary-diff a corrupted download against its original to identify the corruption pattern (UTF-8 replacement characters, double-encoding artifacts, etc.). 3. Check for new middleware, proxy config changes, or library upgrades in the upload path. 4. Pay special attention to any Content-Type or Transfer-Encoding handling changes, multipart form parsing updates, or blob storage client library upgrades.

## Proposed Test Case
Upload a known-good PDF (with a precomputed SHA-256 hash) via the web UI, download it, and assert the hash matches. Repeat for images and spreadsheets. Add this as a regression test in the upload integration test suite.

## Information Gaps
- Exact corruption pattern (hex diff between original and downloaded file) — developer can determine this
- Whether the corruption happens during upload, storage, or download — developer can test each stage
- Whether smaller files are truly unaffected or the reporter just hasn't verified — developer can test with various sizes
- Specific deployment or config change that introduced the regression — check deploy history
