# Conversation: file-upload-corruption x omc-deep-interview

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments definitely sounds disruptive. To help us track this down, could you walk me through the exact steps you're taking when uploading a PDF? Specifically: where in the app are you attaching the file (e.g., task detail view, a comment, drag-and-drop vs. the upload button), and what happens when you try to download and open it afterward — do you get a specific error message from your PDF viewer, or does it just show a blank/garbled page?

---

### reporter

Look, we just upload them the normal way — click the attachment button on the task, pick the PDF, it uploads. Then when someone goes to download it, the PDF viewer says it's damaged or corrupted. I don't know about drag-and-drop vs button, we just use the regular upload. The error says the file is damaged, that's it. This worked fine like a week or two ago and now it's broken. Can you just fix it?

---

### triage-agent

Totally understand, and we want to get this fixed as quickly as possible. One thing that would really help us zero in on the problem: does this happen with *every* PDF you upload, or are some getting through fine? And have you noticed whether other file types (like images or Word docs) are also coming through corrupted, or is it only PDFs?

---

### reporter

Now that you mention it, I think some of the smaller PDFs might be okay? I'm not 100% sure but the ones that are definitely broken are the bigger reports we attach — like our monthly customer summaries and stuff. I haven't really paid attention to other file types honestly. I just need the PDFs to work, that's what our team uses constantly.

---

### triage-agent

That's a really helpful clue about the larger files being the ones affected — that narrows things down a lot.

Could you try one quick thing for us? Pick one of the broken PDFs, note its file size on your computer before uploading, then upload it and download it back. Compare the file size of the original to the downloaded copy. Are they the same size, or is the downloaded version smaller?

This will tell us whether the file is getting cut off during upload/download or whether something else is happening to it in transit.

---

### reporter

Ugh, I don't really have time to run tests for you. I just tried it quickly though and the downloaded file is actually *bigger* than the original, which is weird. The original was like 2.4MB and the downloaded one is bigger. So it's not getting cut off, something is messing with the file. Can you just check what changed recently? This was working fine before.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted after upload — downloaded file is larger than original (likely encoding issue)","problem":"When users upload PDF files via the task attachment button, the files become corrupted and cannot be opened. The downloaded file is larger than the original, indicating the file content is being transformed (not truncated). The issue began approximately 1–2 weeks ago and primarily affects larger PDFs (~2.4MB+). Smaller PDFs may be unaffected.","root_cause_hypothesis":"A recent change in the upload or download pipeline is applying an encoding transformation to binary file data — most likely base64-encoding the file content without properly decoding it on retrieval (or double-encoding it). This would explain why the output file is larger than the input (~33% size increase is characteristic of base64) and why the file is structurally corrupted. Smaller files may appear to work if they fall below a chunked-transfer threshold or a different code path.","reproduction_steps":["Prepare a PDF file approximately 2–3MB in size","Open a task in TaskFlow and click the attachment button","Upload the PDF file","Download the attached file from the task","Compare the file sizes — the download should be noticeably larger than the original","Attempt to open the downloaded PDF — it should report as damaged/corrupted"],"environment":"Not browser-specific as far as known. Affects multiple customers. Issue appeared within the last 1–2 weeks, suggesting a recent deployment introduced the regression.","severity":"high","impact":"Multiple customers are affected. Teams that rely on PDF attachments (e.g., sharing monthly reports) have no workaround and cannot use the attachment feature for their core workflow.","recommended_fix":"Review git history from the last 1–2 weeks for changes to the file upload/download pipeline, particularly: (1) any change to Content-Type or Content-Transfer-Encoding headers, (2) middleware that processes request/response bodies (e.g., a new body-parser or serialization layer that base64-encodes binary data), (3) changes to the storage layer (S3 upload params, database blob handling). Compare the raw bytes of an uploaded file in storage against the original to confirm where the transformation occurs. If base64-encoding is confirmed, ensure binary files are stored and served as raw binary with appropriate Content-Type headers.","proposed_test_case":"Upload a PDF of known size and checksum, download it, and assert that the downloaded file has the same size and checksum as the original. Include test cases at multiple file sizes (100KB, 1MB, 5MB) to identify any size-dependent threshold.","information_gaps":["Exact size threshold at which corruption begins (reporter unsure if smaller PDFs truly work or just haven't been noticed)","Whether non-PDF binary file types (images, Word docs) are also affected","Specific browser and OS used by affected customers","Exact deployment or commit that introduced the regression"]}

---
