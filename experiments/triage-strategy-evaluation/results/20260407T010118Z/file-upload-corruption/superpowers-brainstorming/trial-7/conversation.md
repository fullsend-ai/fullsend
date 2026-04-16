# Conversation: file-upload-corruption x superpowers-brainstorming

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted file uploads are definitely something we want to get to the bottom of quickly.

To help narrow this down: is this happening with **every** PDF you upload, or only certain ones? For example:

1. **Every PDF** — no PDF upload has worked recently
2. **Only large PDFs** — smaller files seem fine
3. **Only certain PDFs** — some work, some don't, and size doesn't seem to be the pattern
4. **Something else**

Also, did this start recently (e.g., after an update), or is it the first time you've tried uploading PDFs?

---

### reporter

Look, I don't know exactly which ones work and which don't. All I know is our team uploads PDFs constantly and a bunch of them are coming through corrupted. It started maybe a week ago? Some of the smaller ones might be okay, I haven't really kept track. We just need this fixed — our customers are waiting on these documents and we can't send them broken files.

---

### triage-agent

Thanks for the detail — that timing is really helpful, and I understand the urgency.

One quick thing that would help us zero in on this: when you download one of the broken PDFs back from TaskFlow, does the file seem:

1. **Much smaller than the original** — like the file got cut off partway through the upload
2. **About the same size as the original** — but still won't open
3. **Not sure** — you haven't compared the sizes

If you happen to have the original and the downloaded copy of the same file, even a rough comparison (e.g., original was 5 MB, downloaded one is 1 MB) would tell us a lot. No worries if you don't have that handy though — just knowing which of the above it looks like is plenty.

---

### reporter

I don't know, I haven't sat there comparing file sizes. I just know they won't open. One of my team members mentioned the corrupted ones looked about the same size but I can't confirm that. Can you just look into what changed on your end? This was working fine until recently.

---

### triage-agent

Totally understand — and you're right, this sounds like something changed on our end, so that's where we'll focus. We'll be looking at recent changes to the file upload pipeline from the past week or two.

One last thing that would help us look in the right place: have you noticed this with **only PDFs**, or are other file types (images, Word docs, spreadsheets, etc.) also coming through broken? If your team mostly uploads PDFs and you're not sure, that's fine too — just let us know.

---

### reporter

We mostly deal with PDFs so that's where we're seeing it. I think someone mentioned an image looking weird too but honestly I'm not sure if that's related or just a different issue. PDFs are the main problem — that's what our customers need and that's what's broken.

---

### triage-agent

[RESOLVED] {"title":"PDF file uploads corrupted since ~1 week ago (files won't open, size appears unchanged)","problem":"PDF files uploaded to tasks via the file attachment feature are being corrupted during upload or storage. Downloaded files cannot be opened — PDF viewers report them as damaged. The issue started approximately one week ago and affects multiple customers. The corrupted files appear to be roughly the same size as the originals, suggesting the data is being altered rather than truncated. Other binary file types (images) may also be affected.","root_cause_hypothesis":"A recent change to the file upload/storage pipeline (within the past 1–2 weeks) is corrupting binary file content while preserving file size. Likely candidates: a change in transfer encoding (e.g., binary being processed as text/UTF-8, introducing byte substitutions), a middleware or proxy change that re-encodes request bodies, a content-type handling change, or a storage layer change that mangles binary streams. The size-preserving nature points toward character encoding corruption rather than truncation or partial upload.","reproduction_steps":["Upload a known-good PDF (≥1 MB) to a task via the file attachment feature","Download the PDF back from the task","Attempt to open the downloaded PDF — expect it to be reported as damaged","Compare the binary content of the original and downloaded files (e.g., diff at hex level) to identify the corruption pattern"],"environment":"Server-side issue affecting all users; not browser- or OS-specific. Onset approximately one week ago.","severity":"high","impact":"Multiple customers are unable to share PDF documents through TaskFlow, blocking their workflows with their own customers. The file attachment feature is a core workflow for these users.","recommended_fix":"1. Review all changes to the file upload pipeline, storage layer, and any reverse proxy or middleware configuration from the past two weeks. 2. Upload a test PDF and compare the stored/downloaded bytes against the original to identify the corruption pattern (byte substitutions suggest encoding issues; shifted bytes suggest framing issues). 3. Check for recent changes to content-type handling, transfer-encoding, or body parsing middleware. 4. If a CDN or proxy sits in front of storage, check for recent configuration changes there as well.","proposed_test_case":"Integration test that uploads a binary PDF file, downloads it back, and asserts byte-for-byte equality with the original. Include files at multiple sizes (100 KB, 5 MB, 20 MB) to catch size-dependent issues.","information_gaps":["Exact file size threshold (if any) below which uploads succeed — reporter indicated smaller files might be okay but wasn't certain","Whether non-PDF binary files (images, Word docs) are also affected — one unconfirmed report of a weird image","Exact date the issue started — reporter estimated 'about a week ago'","Whether the issue affects uploads via API (if applicable) or only the web UI"]}

---
