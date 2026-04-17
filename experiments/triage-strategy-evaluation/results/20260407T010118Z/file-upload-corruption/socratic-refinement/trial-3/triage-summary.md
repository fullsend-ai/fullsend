# Triage Summary

**Title:** PDF attachments (2-5 MB) corrupted after upload — size-dependent, regression ~1 week ago

## Problem
Customers uploading PDF files to tasks find them unopenable ('file is damaged or corrupted'). The corruption is size-dependent: generated reports with images in the 2-5 MB range break, while simple single-page PDFs under 1 MB upload and download correctly. Multiple customers are affected. The issue began approximately one week ago, suggesting a regression.

## Root Cause Hypothesis
A recent change (likely within the past week — check deployment history) introduced a defect in the file upload or storage pipeline that truncates or corrupts files above a certain size threshold. Likely candidates: a new request body size limit or timeout in a proxy/gateway layer, a broken chunked transfer encoding implementation, a misconfigured streaming buffer, or a change to the storage backend (e.g., multipart upload threshold). The fact that files are completely unopenable (rather than partially rendered) suggests the file is being truncated or its binary content is being mangled (e.g., incorrect encoding or content-type handling).

## Reproduction Steps
  1. Upload a multi-page generated PDF with embedded images (~2-5 MB) to a task
  2. Download the same file back from the task
  3. Attempt to open the downloaded file — it will be reported as damaged/corrupted
  4. Repeat with a small single-page PDF (<1 MB) — this should work fine

## Environment
Production environment, multiple customers affected. File type: generated reports (invoices, contracts) with embedded images. No specific browser or OS reported, but the cross-customer nature suggests a server-side issue.

## Severity: high

## Impact
Multiple customers are unable to use the file attachment feature for any non-trivial PDF. This blocks workflows that depend on sharing reports, invoices, and contracts via TaskFlow. The reporter is fielding customer complaints directly.

## Recommended Fix
1. Check deployment/config change history for the past 7-10 days — identify what changed in the upload pipeline, reverse proxy, API gateway, or storage layer. 2. Compare the byte size of a known-corrupted attachment in storage against its original — determine if truncation is occurring and at what offset. 3. Inspect request body size limits, upload timeouts, and chunked transfer encoding across all layers (app server, load balancer, CDN, object storage). 4. Check for any encoding-related changes (e.g., base64 vs binary, content-type handling). 5. Attempt to reproduce with a 3-5 MB PDF in a staging environment.

## Proposed Test Case
Upload PDFs at various sizes (500 KB, 1 MB, 2 MB, 5 MB, 10 MB) with embedded images, download each, and verify byte-for-byte integrity against the original using checksum comparison. This should be added as an automated integration test for the attachment pipeline.

## Information Gaps
- No specific task ID for direct log correlation (reporter would need to look one up — can be found by querying for recent attachment uploads in the 2-5 MB range that may show errors)
- Exact file sizes before and after upload not compared (developer can check this server-side)
- Browser and OS of affected users not identified (likely irrelevant given server-side pattern)
- Exact size threshold where corruption begins is not pinpointed (developer can determine via controlled testing)
