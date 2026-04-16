# Conversation: file-upload-corruption x omc-deep-interview

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments definitely sounds disruptive. To help us narrow this down: when you download a corrupted PDF and compare it to the original, is the file size roughly the same, noticeably smaller, or larger? And does the PDF viewer give you a specific error message (e.g., "file is damaged and could not be repaired", "not a valid PDF", etc.)? Also, does this happen with every PDF you upload, or only some — and if only some, have you noticed any pattern (file size, how the PDF was created, etc.)?

---

### reporter

I don't know about file sizes compared to originals, I haven't checked that. The error is something like "file is damaged" when you try to open it. Look, it doesn't happen with EVERY pdf — I've noticed smaller ones seem to be fine actually. But the bigger reports our customers attach are the ones that come through broken. Can you just fix this? Our team is blocked.

---

### triage-agent

Thanks for those details — that size pattern is a really useful clue. Do you have a rough sense of how big the PDFs are that break? Even a ballpark helps (e.g., over 5 MB, over 10 MB?). And one more thing: are you uploading these through the web interface (drag-and-drop or the attach button), through the API, or some other way?

---

### reporter

I don't know the exact sizes, they're like... regular PDF reports, maybe a few MB? I'm not a tech person, I just know the big ones break. We use the web interface, the attach button on tasks. Can you please prioritize this? We've had customers complaining for about a week now.

---

### triage-agent

Totally understand the urgency — we want to get this fixed for your team.

One thing that would really help us zero in on the cause: did PDF attachments used to work fine for these same kinds of reports, and then something broke about a week ago? Or has this always been an issue with larger files and you're only now reporting it?

If it did recently break, do you recall roughly when — even something like "it was working last Monday but broken by Wednesday" would help us check what changed on our end.

---

### reporter

Yeah it was working fine before! Like I said, customers have been complaining for about a week. Before that we never had this problem with PDFs. I don't remember the exact day it started but it was maybe last Monday or Tuesday? It just suddenly started happening. Nothing changed on our end — we're uploading the same kinds of reports we always do.

---

### triage-agent

Completely understand — we're treating this as high priority.

To make sure our developers can reproduce this right away: could you try uploading a PDF that you know breaks, and then a smaller one that works, and let us know the approximate file sizes of each? You can check by right-clicking the file and looking at properties/info. Even rough numbers like "the 1 MB one worked but the 3 MB one didn't" would help us find the exact cutoff.

Also — which browser are you using (Chrome, Firefox, Safari, Edge)? Just want to rule that out quickly.

---

### reporter

Ugh, fine. I just checked one that worked and it was like 800KB. One that's broken is about 2MB. We mostly use Chrome but I don't think that matters, it's broken for everyone on the team regardless of browser. Look, it's clearly something on your end that changed — can someone just look at whatever was updated last week?

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted on upload above ~1MB (regression, ~1 week old)","problem":"PDF files uploaded via the web interface's attach button are silently corrupted during or after upload. The resulting files cannot be opened — PDF viewers report 'file is damaged.' The corruption is size-dependent: files around 800KB upload correctly, while files around 2MB and above are broken. This is a regression that began approximately one week ago (estimated Monday/Tuesday of the prior week). No changes were made on the customer side.","root_cause_hypothesis":"A server-side change deployed approximately one week ago is corrupting file uploads above a size threshold somewhere between 800KB and 2MB. Likely candidates: (1) a new or modified upload size limit or chunked-upload boundary that truncates or re-encodes the file, (2) a middleware or proxy change (e.g., body-parser limit, nginx client_max_body_size, or a new WAF rule) that silently truncates the request body, (3) a change in how binary data is handled during storage (e.g., base64 encoding applied where raw binary is expected, or a streaming/multipart parsing regression).","reproduction_steps":["Obtain any PDF file approximately 2MB or larger","Log into TaskFlow via the web interface (Chrome or any browser)","Navigate to any task and click the attach button","Upload the PDF","Download the uploaded PDF","Attempt to open it — it should report 'file is damaged'","Repeat with a PDF under 800KB — this should succeed","Compare the uploaded file's byte content or size against the original to identify where corruption/truncation occurs"],"environment":"Web interface (attach button on tasks), all browsers (confirmed Chrome; team reports it affects everyone regardless of browser). Server-side regression — no client-side changes.","severity":"high","impact":"Multiple customers are affected. The reporting team uses file attachments heavily and considers themselves blocked. Complaints have been ongoing for approximately one week, indicating sustained impact across their customer base.","recommended_fix":"1. Review all deployments from the week the regression started (Monday/Tuesday, ~1 week before report). 2. Compare the byte length of an uploaded 2MB PDF against its original — determine whether the file is truncated, re-encoded, or otherwise altered. 3. Check upload pipeline configuration: reverse proxy limits (nginx/Apache), application-level body size limits, multipart parsing libraries, and storage-layer write logic. 4. Check if any middleware was added or updated that processes request bodies (compression, encoding, WAF, antivirus scanning). 5. Verify binary stream handling end-to-end — ensure no step treats the binary payload as text (e.g., UTF-8 encoding of a binary stream).","proposed_test_case":"Upload PDFs at several sizes (500KB, 1MB, 1.5MB, 2MB, 5MB, 10MB) via the web attach button, then download each and verify byte-for-byte integrity against the originals. This identifies the exact corruption threshold and confirms the fix. Automate as an integration test that asserts uploaded_file_hash == downloaded_file_hash for files above and below the boundary.","information_gaps":["Exact size threshold where corruption begins (known to be between 800KB and 2MB)","Whether the corrupted file is truncated, larger than expected, or the same size but with altered content","Whether API-based uploads are also affected (only web UI confirmed)","Exact deployment date that introduced the regression"]}

---
