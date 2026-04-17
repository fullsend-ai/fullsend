# Conversation: file-upload-corruption x socratic-refinement

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments sound really disruptive, especially if your team relies on them heavily.

To help us understand what's going on: could you walk me through what you were doing when you noticed the corruption? For example, what kind of PDFs are these (scanned documents, generated reports, forms?), roughly how large are they, and did this start happening recently or has it been an ongoing issue? If it's recent, do you recall anything that changed around the time it started — a software update, a change in how you're uploading, etc.?

---

### reporter

Look, they're mostly generated reports — invoices, contracts, that kind of thing. Some are pretty big, like multi-page documents with images in them. It started maybe a week ago? I don't know what changed on your end but something broke. The small quick PDFs seem to work fine actually, it's the bigger ones that come through mangled. Can you just fix it? Our customers are complaining and I'm the one who has to deal with it.

---

### triage-agent

That's really helpful — the pattern you're describing (big files break, small ones are fine) gives us a strong lead to work with.

Could you tell me a bit more about what you see when you try to open one of the corrupted files? For instance, does the PDF viewer show a blank page, a partial document (like the first few pages are fine but the rest is garbled), or does it refuse to open entirely? And if you compare the file size of the original PDF before uploading to the size of the file you download back from TaskFlow, are they noticeably different — like the downloaded one is smaller than it should be?

That'll help us figure out whether the file is getting cut off during upload or something else is going wrong.

---

### reporter

It just says the file is damaged or corrupted and won't open at all. I haven't compared file sizes, I don't have time to be doing forensics on this — I've got a queue of angry customers. The files are broken, that's all I know. Can someone look at what changed on your end a week ago? That's when this started.

---

### triage-agent

Totally understand — you shouldn't have to play detective here, and I'm going to get this to the engineering team. One last thing that'll help them move fast: could you share a task ID (or a link to a specific task) where one of these broken uploads happened? That lets them pull up the exact server logs and see what went wrong without guessing. And roughly how big are the files that break — are we talking a few megabytes, or more like 20-50 MB+? Once I have that, I'll have everything I need to hand this off.

---

### reporter

I don't have a task ID handy right now, I'd have to dig through tickets to find one. The files that break are probably like 2-5 MB range, the bigger reports with images and stuff. The ones that work are the little one-pagers, probably under a meg. Just look at whatever you changed a week ago, that's when it broke.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments (2-5 MB) corrupted after upload — size-dependent, regression ~1 week ago","problem":"Customers uploading PDF files to tasks find them unopenable ('file is damaged or corrupted'). The corruption is size-dependent: generated reports with images in the 2-5 MB range break, while simple single-page PDFs under 1 MB upload and download correctly. Multiple customers are affected. The issue began approximately one week ago, suggesting a regression.","root_cause_hypothesis":"A recent change (likely within the past week — check deployment history) introduced a defect in the file upload or storage pipeline that truncates or corrupts files above a certain size threshold. Likely candidates: a new request body size limit or timeout in a proxy/gateway layer, a broken chunked transfer encoding implementation, a misconfigured streaming buffer, or a change to the storage backend (e.g., multipart upload threshold). The fact that files are completely unopenable (rather than partially rendered) suggests the file is being truncated or its binary content is being mangled (e.g., incorrect encoding or content-type handling).","reproduction_steps":["Upload a multi-page generated PDF with embedded images (~2-5 MB) to a task","Download the same file back from the task","Attempt to open the downloaded file — it will be reported as damaged/corrupted","Repeat with a small single-page PDF (<1 MB) — this should work fine"],"environment":"Production environment, multiple customers affected. File type: generated reports (invoices, contracts) with embedded images. No specific browser or OS reported, but the cross-customer nature suggests a server-side issue.","severity":"high","impact":"Multiple customers are unable to use the file attachment feature for any non-trivial PDF. This blocks workflows that depend on sharing reports, invoices, and contracts via TaskFlow. The reporter is fielding customer complaints directly.","recommended_fix":"1. Check deployment/config change history for the past 7-10 days — identify what changed in the upload pipeline, reverse proxy, API gateway, or storage layer. 2. Compare the byte size of a known-corrupted attachment in storage against its original — determine if truncation is occurring and at what offset. 3. Inspect request body size limits, upload timeouts, and chunked transfer encoding across all layers (app server, load balancer, CDN, object storage). 4. Check for any encoding-related changes (e.g., base64 vs binary, content-type handling). 5. Attempt to reproduce with a 3-5 MB PDF in a staging environment.","proposed_test_case":"Upload PDFs at various sizes (500 KB, 1 MB, 2 MB, 5 MB, 10 MB) with embedded images, download each, and verify byte-for-byte integrity against the original using checksum comparison. This should be added as an automated integration test for the attachment pipeline.","information_gaps":["No specific task ID for direct log correlation (reporter would need to look one up — can be found by querying for recent attachment uploads in the 2-5 MB range that may show errors)","Exact file sizes before and after upload not compared (developer can check this server-side)","Browser and OS of affected users not identified (likely irrelevant given server-side pattern)","Exact size threshold where corruption begins is not pinpointed (developer can determine via controlled testing)"]}

---
