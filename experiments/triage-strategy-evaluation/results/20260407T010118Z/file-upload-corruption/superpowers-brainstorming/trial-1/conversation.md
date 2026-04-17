# Conversation: file-upload-corruption x superpowers-brainstorming

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments are definitely something we want to get to the bottom of quickly.

To help us narrow down the cause, can you tell us: is this happening with **every** PDF you upload, or only certain ones? For example:

1. **Every PDF, every time** — no PDF upload has worked recently
2. **Only certain PDFs** — some work fine, others come through corrupted
3. **Intermittent** — the same PDF sometimes works and sometimes doesn't
4. **Something else**

This will help us figure out whether we're looking at a systemic issue or something specific to certain files.

---

### reporter

It's option 2 — some PDFs work fine and others don't. I haven't figured out the pattern honestly. I just know that some of our customers' PDFs open fine after uploading and others are completely broken. It's really frustrating because we can't predict which ones will work. Can you just fix it? This has been going on for like a week now and our team is getting complaints.

---

### triage-agent

That's really helpful, thank you — and I understand the urgency, we'll get this sorted.

One thing that often causes this pattern is file size. Could you check whether the PDFs that come through broken tend to be **larger** than the ones that work? For example:

1. **Yes** — the broken ones are noticeably larger files (e.g., multi-page scans, image-heavy documents)
2. **No** — I've seen small PDFs break too, size doesn't seem to matter
3. **Not sure** — but I can check a few and get back to you

If you happen to know the approximate size of a working PDF vs. a broken one (even rough — like "50 KB vs. 10 MB"), that would be especially useful.

---

### reporter

Yeah actually now that you mention it, I think the ones that work are the smaller ones. Like quick one-page PDFs seem fine but the bigger reports and scanned documents are the ones that come through corrupted. I don't know exact sizes but the small simple ones work and the big ones don't. Can you just roll back whatever changed last week? This wasn't happening before.

---

### triage-agent

[RESOLVED] {"title":"Large PDF attachments corrupted on upload (regression, ~1 week old)","problem":"PDF files attached to tasks are corrupted and unopenable after upload. The issue selectively affects larger files (multi-page scans, image-heavy reports) while small single-page PDFs upload correctly. Multiple customers are affected. The behavior is a regression that began approximately one week ago.","root_cause_hypothesis":"A recent change (within the last week) to the file upload pipeline is truncating or corrupting large files. Most likely candidates: a new file size limit was introduced, a request/upload timeout was reduced, chunked transfer encoding was changed, or a streaming/buffering change is cutting off uploads that exceed a certain size or duration.","reproduction_steps":["Upload a small, simple single-page PDF (e.g., <500 KB) to a task — expect success","Upload a large multi-page or image-heavy PDF (e.g., >5 MB) to a task","Download the uploaded large PDF and attempt to open it","Observe that the file is reported as damaged/corrupted by the PDF viewer","Compare the uploaded file size to the original to check for truncation"],"environment":"Production environment, affects multiple customers. No specific browser or OS constraint reported — likely server-side.","severity":"high","impact":"Multiple customers are unable to use the file attachment feature for large PDFs. The team is receiving complaints and has been impacted for approximately one week. Core workflow (attaching documents to tasks) is partially broken.","recommended_fix":"1. Review all deployments and config changes from the past ~10 days affecting the upload pipeline (file upload endpoints, reverse proxy config, storage layer). 2. Check for newly introduced file size limits, reduced timeouts, or changes to multipart/chunked upload handling. 3. Test uploading files at various sizes to identify the exact threshold where corruption begins. 4. Compare a corrupted uploaded file against the original (file size, binary diff) to determine if the file is truncated or garbled — this distinguishes timeout/size-limit issues from encoding issues. 5. If a specific change is identified, roll it back or fix the boundary condition.","proposed_test_case":"Upload PDFs of increasing sizes (100 KB, 1 MB, 5 MB, 10 MB, 25 MB) and verify that each downloaded file matches the original byte-for-byte (SHA-256 checksum comparison). This test should cover both simple text PDFs and image-heavy scanned documents.","information_gaps":["Exact file size threshold where corruption begins","Whether corrupted files are truncated (smaller than original) or garbled (same size but different content)","Which specific deployment or config change introduced the regression"]}

---
