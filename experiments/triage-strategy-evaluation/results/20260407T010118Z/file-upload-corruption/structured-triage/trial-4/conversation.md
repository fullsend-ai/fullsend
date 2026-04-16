# Conversation: file-upload-corruption x structured-triage

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted file uploads are definitely something we want to get to the bottom of quickly.

Could you walk me through the exact steps you're taking when uploading a PDF? For example:
- Where in the app are you uploading (task detail view, drag-and-drop, etc.)?
- Roughly how large are the affected files?
- Does this happen with every PDF you upload, or only certain ones?

This will help us reproduce the issue on our end.

---

### reporter

We upload from the task detail view, the little paperclip icon. I don't know the exact sizes, they're just normal PDFs — reports, contracts, that kind of thing. It doesn't happen with every single PDF actually, now that you mention it. Some of the smaller ones seem to be okay? But honestly I haven't been tracking which ones work and which don't, I just know a lot of them are coming through broken and my team is getting frustrated. Can you just look into it? This started maybe a week or so ago and it was working fine before that.

---

### triage-agent

Thanks for those details — the size correlation is a really useful clue. Knowing it started about a week ago also helps us narrow things down.

Could you let us know what browser and operating system you're using when uploading? And if you happen to know what version of TaskFlow you're on (you can usually find this under Settings → About), that would be helpful too. We want to make sure we're testing against the same setup.

---

### reporter

I don't know, Chrome I think? Whatever the latest version is. And I have no idea what version of TaskFlow we're on, that's an IT question. Look, this isn't a browser issue — it was working fine last week and nothing changed on our end. Can someone just look at the upload system? Multiple people on my team are hitting this across different computers.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted after upload — likely size-dependent regression","problem":"PDF files uploaded via the task detail view attachment feature are corrupted and cannot be opened. The PDF viewer reports the file as damaged. This affects multiple users across different computers and started approximately one week ago. Smaller PDFs appear to upload successfully; larger ones (reports, contracts) are more likely to be corrupted.","root_cause_hypothesis":"A recent change (approximately one week ago) likely introduced a regression in the file upload pipeline that truncates or corrupts larger uploads. Possible causes include: a misconfigured upload size limit or chunking change, a encoding/transfer issue (e.g., binary file not handled as binary), or a change in the storage backend or processing middleware that damages files above a certain size threshold.","reproduction_steps":["Open a task in the task detail view","Click the paperclip icon to attach a file","Upload a PDF file (try one that is several MB — reports or contracts)","After upload completes, download the attached file","Attempt to open the downloaded PDF — it should report as damaged/corrupted","Repeat with a very small PDF (< 1 page) to confirm it uploads correctly"],"environment":"Chrome (latest) reported, but issue reproduces across multiple users and different computers, suggesting it is not browser- or OS-specific. TaskFlow version unknown — check what was deployed approximately one week ago.","severity":"high","impact":"Multiple customers are affected. Teams that rely on file attachments for sharing reports and contracts cannot use the feature reliably, forcing workarounds and disrupting workflows.","recommended_fix":"Review all changes to the file upload pipeline, storage backend, and any related middleware or proxy configuration deployed in the past 1-2 weeks. Specifically investigate: (1) any change to multipart upload handling or chunked transfer encoding, (2) upload size limits or body-parser configuration changes, (3) binary vs. text encoding in the upload stream, (4) CDN or reverse proxy changes that may alter request bodies above a size threshold. Compare byte-for-byte a corrupted uploaded file against its original to identify where truncation or corruption occurs.","proposed_test_case":"Upload PDF files of varying sizes (100KB, 1MB, 5MB, 10MB) via the task detail view and verify that each downloaded attachment is byte-identical to the original. This should be an automated integration test that compares SHA-256 hashes of the original and retrieved files.","information_gaps":["Exact file size threshold where corruption begins","Whether non-PDF file types (images, DOCX) are also affected","Exact TaskFlow version / recent deployment that correlates with the onset"]}

---
