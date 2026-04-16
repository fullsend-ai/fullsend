# Conversation: file-upload-corruption x omc-deep-interview

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted uploads are definitely something we want to get to the bottom of quickly.

To start narrowing this down: when you say the PDFs come through broken, can you tell me a bit more about what you're seeing? Specifically:

- Does this happen with **every** PDF you upload, or only some? If only some, is there anything those files have in common (file size, how they were created, etc.)?
- When you download the corrupted file back from TaskFlow, is the file size noticeably different from the original (e.g., smaller, or exactly the same)?

This will help us figure out whether the file is being truncated, re-encoded, or something else entirely.

---

### reporter

Look, I don't have time to go through every single PDF and compare file sizes. What I can tell you is that the smaller PDFs seem to work fine — it's the bigger ones that are the problem. I haven't measured exact sizes or anything. The files are just broken when we download them back. Can you just look into this? Our team is stuck.

---

### triage-agent

Totally understand — we'll dig into this. Just one quick thing that'll help us reproduce it on our end: roughly how big are the PDFs that are failing? (e.g., are we talking 5 MB, 20 MB, 100 MB?) And are you uploading through the web app, the mobile app, or the API? That's all we need to start investigating.

---

### reporter

I don't know, maybe a few MB? They're regular business documents — contracts, reports, that kind of thing. We use the web app. Can someone please just look at this? It was working fine like a week or two ago.

---

### triage-agent

Totally hear you — we're on it. One last thing before we start digging into the code: have you tried uploading any other large files (Word docs, images, ZIPs, etc.) recently? Do those come through fine, or is the corruption happening with all larger attachments — not just PDFs?

This tells us whether something changed in how we handle uploads generally versus something specific to PDF processing, and it'll point our developers at the right part of the system right away.

---

### reporter

Honestly I mostly deal with PDFs so I can't say for sure. I think someone on my team mentioned an image looked weird the other day but I didn't really look into it. Look, the main issue is the PDFs — that's what's blocking us. It was definitely working fine before, something changed on your end recently.

---

### triage-agent

[RESOLVED] {"title":"Large file uploads corrupted on download (PDFs confirmed, possibly other types)","problem":"Files uploaded to tasks via the web app are corrupted when downloaded. The corruption is size-dependent: smaller PDFs work fine, but larger ones (reporter estimates 'a few MB' and up for business documents like contracts and reports) are broken and cannot be opened. An anecdotal report suggests at least one image upload may also have been affected. This is a regression — uploads were working correctly until approximately 1-2 weeks ago.","root_cause_hypothesis":"A recent change (within the last 1-2 weeks) to the upload pipeline is corrupting larger files. The size-dependent nature and cross-format hints point to a general upload handling issue rather than PDF-specific processing — likely a bug in chunked upload assembly, multipart form handling, stream buffering, or a newly introduced file size limit that silently truncates rather than rejecting. A misconfigured reverse proxy (e.g., Nginx client_max_body_size) or a change to the storage layer (S3 multipart upload config) could also explain this.","reproduction_steps":["Upload a small PDF (under 1 MB) to a task via the web app — confirm it downloads correctly","Upload a larger PDF (3-5 MB+, e.g., a multi-page scanned document or report) to a task via the web app","Download the uploaded file and attempt to open it","Compare the uploaded and downloaded file sizes and checksums (MD5/SHA) to identify truncation or byte corruption","Repeat with a large image or other binary file to confirm whether corruption is format-independent"],"environment":"Web application (browser not specified). Multiple customers affected. No information on specific browser, OS, or network conditions.","severity":"high","impact":"Multiple customers are blocked. Teams that rely heavily on file attachments (the reporter's primary workflow involves contracts and reports) cannot use the feature. This is a regression from a previously working state, affecting core functionality.","recommended_fix":"1. Review all deployments and config changes from the past 2 weeks for anything touching the upload pipeline (upload endpoints, storage service, reverse proxy config, CDN settings). 2. Compare uploaded vs. downloaded file byte-for-byte at each stage of the pipeline (client → server → storage → retrieval) to identify where corruption is introduced. 3. Check for changes to multipart upload handling, chunked transfer encoding, stream processing, or file size limits. 4. Look at infrastructure changes (proxy/load balancer config, storage backend) that could silently truncate or re-encode binary data.","proposed_test_case":"Upload files of varying sizes (100 KB, 1 MB, 5 MB, 10 MB, 50 MB) across multiple formats (PDF, PNG, DOCX, ZIP). After download, assert that the SHA-256 checksum of the downloaded file matches the original. This should be an automated integration test against the upload/download API.","information_gaps":["Exact file size threshold where corruption begins (developer can determine via systematic testing)","Whether non-PDF file types are also affected (anecdotal hint suggests yes, but unconfirmed)","Specific browser and OS used by affected customers","Whether the stored file (in object storage / on disk) is already corrupted or if corruption happens on download"]}

---
