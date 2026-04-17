# Triage Summary

**Title:** File upload corruption for PDFs (and possibly other files) above a size threshold — recent regression

## Problem
PDF files uploaded to tasks are corrupted and cannot be opened. The issue affects larger files (multi-page reports, contracts — estimated a few MB) while small single-page PDFs upload correctly. Multiple customers are affected. There is also an unconfirmed report of an image appearing corrupted, suggesting the issue may not be PDF-specific.

## Root Cause Hypothesis
A deployment in the past ~1 week likely introduced a bug in the file upload pipeline that corrupts files above a certain size threshold. Prime suspects: (1) a change to chunked transfer encoding or multipart upload handling that truncates or mangles data beyond a chunk boundary, (2) a misconfigured upload size limit or buffer size causing partial writes, (3) a change in encoding/content-type handling that corrupts binary data above a certain size. The fact that small files work and large files don't strongly suggests a threshold-dependent code path (e.g., files small enough to upload in a single request succeed, while those that trigger chunked/multipart processing are corrupted).

## Reproduction Steps
  1. Upload a small PDF (~100KB, single page) to a task — confirm it downloads and opens correctly
  2. Upload a larger PDF (~2-5MB, multi-page report or contract) to a task
  3. Download the uploaded file and attempt to open it
  4. Compare the file size and checksum (md5/sha256) of the original and downloaded files to identify where corruption occurs
  5. Test with a non-PDF binary file (e.g., a PNG image) of similar size to determine if the issue is format-specific or general

## Environment
Not constrained to a specific browser or OS — reported across multiple customers. TaskFlow production environment. Issue emerged approximately one week before report date.

## Severity: high

## Impact
Multiple customers are unable to use file attachments for standard business documents (contracts, reports). The file attachment feature is described as heavily used. No workaround exists for affected files — they simply cannot be opened. This is blocking customer workflows.

## Recommended Fix
1. Review all deployments from the past 7-10 days that touched the file upload/download pipeline, file storage layer, or related middleware (reverse proxies, API gateways with body size limits). 2. Compare uploaded vs. downloaded file checksums to confirm corruption happens during upload (not download). 3. Identify the size threshold at which corruption begins — upload test files at 500KB, 1MB, 2MB, 5MB, 10MB increments. 4. Inspect the upload path for chunked transfer or multipart handling changes — look for buffer size changes, encoding mismatches, or stream handling bugs. 5. Check for infrastructure changes (reverse proxy config, cloud storage SDK version bumps, content-type header changes) that may affect binary data transfer.

## Proposed Test Case
Upload binary files at size increments (100KB, 500KB, 1MB, 2MB, 5MB, 10MB, 25MB) in both PDF and non-PDF formats. For each, verify the downloaded file is byte-identical to the original (checksum match). This test should be automated and added to the upload pipeline's integration test suite to prevent future regressions.

## Information Gaps
- Exact size threshold where corruption begins (discoverable via controlled upload testing)
- Whether the corruption occurs during upload, storage, or download (discoverable by comparing stored file checksums to originals)
- Whether non-PDF files are also affected (reporter was uncertain; one anecdotal report of a corrupted image)
- Specific browser and OS combinations affected (likely irrelevant if server-side, but worth confirming)
- Exact deployment or commit that introduced the regression (discoverable via deployment history review)
