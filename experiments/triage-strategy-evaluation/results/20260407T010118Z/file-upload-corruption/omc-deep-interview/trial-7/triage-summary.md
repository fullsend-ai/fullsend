# Triage Summary

**Title:** PDF attachments corrupted on upload above ~1MB (regression, ~1 week old)

## Problem
PDF files uploaded via the web interface's attach button are silently corrupted during or after upload. The resulting files cannot be opened — PDF viewers report 'file is damaged.' The corruption is size-dependent: files around 800KB upload correctly, while files around 2MB and above are broken. This is a regression that began approximately one week ago (estimated Monday/Tuesday of the prior week). No changes were made on the customer side.

## Root Cause Hypothesis
A server-side change deployed approximately one week ago is corrupting file uploads above a size threshold somewhere between 800KB and 2MB. Likely candidates: (1) a new or modified upload size limit or chunked-upload boundary that truncates or re-encodes the file, (2) a middleware or proxy change (e.g., body-parser limit, nginx client_max_body_size, or a new WAF rule) that silently truncates the request body, (3) a change in how binary data is handled during storage (e.g., base64 encoding applied where raw binary is expected, or a streaming/multipart parsing regression).

## Reproduction Steps
  1. Obtain any PDF file approximately 2MB or larger
  2. Log into TaskFlow via the web interface (Chrome or any browser)
  3. Navigate to any task and click the attach button
  4. Upload the PDF
  5. Download the uploaded PDF
  6. Attempt to open it — it should report 'file is damaged'
  7. Repeat with a PDF under 800KB — this should succeed
  8. Compare the uploaded file's byte content or size against the original to identify where corruption/truncation occurs

## Environment
Web interface (attach button on tasks), all browsers (confirmed Chrome; team reports it affects everyone regardless of browser). Server-side regression — no client-side changes.

## Severity: high

## Impact
Multiple customers are affected. The reporting team uses file attachments heavily and considers themselves blocked. Complaints have been ongoing for approximately one week, indicating sustained impact across their customer base.

## Recommended Fix
1. Review all deployments from the week the regression started (Monday/Tuesday, ~1 week before report). 2. Compare the byte length of an uploaded 2MB PDF against its original — determine whether the file is truncated, re-encoded, or otherwise altered. 3. Check upload pipeline configuration: reverse proxy limits (nginx/Apache), application-level body size limits, multipart parsing libraries, and storage-layer write logic. 4. Check if any middleware was added or updated that processes request bodies (compression, encoding, WAF, antivirus scanning). 5. Verify binary stream handling end-to-end — ensure no step treats the binary payload as text (e.g., UTF-8 encoding of a binary stream).

## Proposed Test Case
Upload PDFs at several sizes (500KB, 1MB, 1.5MB, 2MB, 5MB, 10MB) via the web attach button, then download each and verify byte-for-byte integrity against the originals. This identifies the exact corruption threshold and confirms the fix. Automate as an integration test that asserts uploaded_file_hash == downloaded_file_hash for files above and below the boundary.

## Information Gaps
- Exact size threshold where corruption begins (known to be between 800KB and 2MB)
- Whether the corrupted file is truncated, larger than expected, or the same size but with altered content
- Whether API-based uploads are also affected (only web UI confirmed)
- Exact deployment date that introduced the regression
