# Conversation: file-upload-corruption x socratic-refinement

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments definitely sounds disruptive, especially if you rely on them heavily.

To help me understand the full picture: can you walk me through what you were doing when you first noticed this? For example, were you uploading a specific PDF, and did you notice the corruption right away when you tried to re-download it, or did someone else flag it later? Also, do you know roughly when this started happening — was it working fine at some point, and if so, did anything change around the time it broke (e.g., a browser update, a change in how you're generating the PDFs, etc.)?

---

### reporter

Look, I don't remember exactly when I first noticed it — maybe a week ago? One of my team members came to me saying a customer's PDF was broken. Then another one. Then another. It's not like I was sitting there testing uploads, I just know that PDFs that customers send us are coming through damaged. We download them and they won't open. Nothing changed on our end — same browser, same PDFs. These are normal PDFs that worked fine before. Can you just look into what changed on your side recently?

---

### triage-agent

I hear you — it sounds like this crept up without any change on your side, and I want to help get to the bottom of it.

Here's what would really help me point the developers in the right direction: when you download one of these broken PDFs, does the file size look roughly the same as the original, or is it noticeably smaller (or even zero)? And is it *every* PDF that's coming through damaged now, or are some still making it through fine? If some work, anything you've noticed that's different about the ones that do — like smaller files working but larger ones breaking, for instance?

---

### reporter

I haven't really compared file sizes of the broken ones vs originals, I don't have time to be doing file comparisons. But now that you mention it — yeah, I think the smaller PDFs might actually be fine? Like quick one-page things seem to come through okay. It's the bigger reports and contracts that are messed up. But I'm not 100% sure on that, I just want this fixed — we have customers waiting on these documents.

---

### triage-agent

Completely understand the urgency — this is exactly the kind of pattern the team can dig into.

One quick thing that would help narrow this down fast: when your team uploads one of these larger PDFs, does the upload itself appear to complete normally (progress bar finishes, no error messages), or does anything seem off during the upload — like it hanging, taking unusually long, or throwing an error? And are these being uploaded through the web interface, or through some other method like an API or email integration?

---

### reporter

The upload seems to work fine — no errors, no weird progress bar stuff. It just completes like normal. Then when you go to download the file it's garbage. We upload through the web interface, that's it. Nothing fancy.

---

### triage-agent

[RESOLVED] {"title":"Large PDF attachments silently corrupted on upload — small PDFs unaffected","problem":"PDF files attached to tasks via the web interface upload successfully (no errors, progress bar completes normally), but when downloaded, larger PDFs are damaged and cannot be opened. Smaller PDFs (e.g., single-page documents) appear unaffected. Multiple customers are impacted. The issue started approximately one week ago with no client-side changes.","root_cause_hypothesis":"A server-side change introduced roughly one week ago is corrupting large file uploads during processing or storage. Likely candidates: (1) a regression in chunked upload reassembly that truncates or misorders chunks for files exceeding a threshold, (2) a newly introduced or lowered request body size limit (e.g., in a reverse proxy, API gateway, or web server config) that silently truncates the upload, or (3) a timeout in an upload processing pipeline that cuts off large files before they finish writing to storage.","reproduction_steps":["Upload a PDF larger than ~1-2 MB to a task via the web interface","Wait for the upload to complete (should show success, no errors)","Download the uploaded file","Attempt to open the downloaded PDF — it should be reported as damaged/corrupted","Repeat with a small single-page PDF to confirm it uploads and downloads correctly"],"environment":"Web interface (browser-based upload). No API or email integration involved. No recent changes on the client side (browser, PDF generation). Issue began approximately one week ago.","severity":"high","impact":"Multiple customers are unable to exchange PDF documents (contracts, reports) through the task attachment workflow. The reporter's team relies heavily on this feature and has customers waiting on documents. Silent corruption (no error feedback) means users only discover the problem after downloading, wasting time and eroding trust.","recommended_fix":"1. Review all server-side changes deployed in the past 1-2 weeks that touch the file upload pipeline, storage layer, or reverse proxy/gateway configuration. 2. Check for recently introduced or changed request size limits (nginx client_max_body_size, API gateway payload limits, application-level multipart limits). 3. Inspect chunked upload handling — verify that all chunks are reassembled in order and completely for large files. 4. Compare the byte content of an uploaded-then-downloaded large PDF against the original to identify where truncation or corruption occurs (beginning, end, or mid-file). 5. Check upload processing timeouts that might cut off writes for larger files.","proposed_test_case":"Upload PDFs of varying sizes (100KB, 1MB, 5MB, 10MB, 25MB) via the web interface, then download each and perform a byte-for-byte comparison (e.g., checksum) against the original. The test passes when all downloaded files are identical to their originals. Additionally, add an integration test that uploads a file above the suspected size threshold and asserts the downloaded content matches.","information_gaps":["Exact file size threshold where corruption begins (developers can determine this through controlled testing)","Whether the corruption is deterministic (same file always corrupts the same way) or non-deterministic","Whether the stored file on the server/object store is already corrupted, or only the download path introduces corruption","Specific server-side deployments or configuration changes made in the past 1-2 weeks"]}

---
