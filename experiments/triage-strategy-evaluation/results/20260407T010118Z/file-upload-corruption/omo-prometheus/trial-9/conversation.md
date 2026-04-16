# Conversation: file-upload-corruption x omo-prometheus

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted file attachments are definitely something we want to get to the bottom of quickly.

To start narrowing this down: **Was PDF upload working correctly before, and if so, when did you first notice the corruption?** Also, is this happening with *every* PDF you upload, or only certain ones (e.g., larger files, scanned documents, PDFs with specific features like forms or signatures)?

---

### reporter

It was working fine until maybe a week ago? Something changed. And yeah it's not every PDF — I've noticed some smaller ones seem to go through okay, but the bigger reports our customers send are the ones that keep coming back corrupted. I don't know what changed, I just need this fixed because our team is dealing with angry customers every day now.

---

### triage-agent

That's really helpful — a regression tied to file size is a strong signal.

Can you help me pin down the threshold? Roughly how large are the PDFs that come through corrupted — are we talking 5 MB, 20 MB, 100 MB? And if you upload the *same* large PDF twice, does it come back corrupted both times, or is it intermittent?

Also, when you say the PDF viewer says the file is "damaged" — is that when downloading and opening it locally, or when previewing it inside TaskFlow? If you download the corrupted file, is it the same file size as the original, or noticeably smaller/larger?

---

### reporter

I don't know the exact sizes, I'm not sitting here measuring files. They're regular business reports, maybe 2-5 MB? Could be bigger, I don't know. And yes it happens every time with those files, it's not random. When we download them they're just broken — the PDF viewer says damaged. I think the file size looks about the same but honestly I haven't compared. Look, can someone just fix this? It's been a week and our customers are furious.

---

### triage-agent

I hear you, and I want to make sure this gets routed to a developer with enough detail to fix it quickly — that's the fastest path to a resolution.

One question that will really narrow this down: **Does the same corruption happen if you upload a non-PDF file of similar size — for example, a Word document or a large image?** If you can try that with one file, it will tell us whether the problem is in how we handle PDFs specifically or in the upload system itself, which points the developer at completely different code.

---

### reporter

I don't know, I haven't tried that. We mostly deal with PDFs. I guess I could try uploading a big image or something but honestly that's not really my job — I just need the PDFs to work. Maybe someone on your end can test that? You guys have access to the system.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted on download — size-dependent regression introduced ~1 week ago","problem":"PDF files uploaded to tasks are corrupted when downloaded. The PDF viewer reports the file as damaged. The issue is reproducible (not intermittent), affects multiple customers, and appears limited to larger files (~2-5 MB+). Smaller PDFs upload and download correctly. This is a regression — the feature was working correctly until approximately one week ago.","root_cause_hypothesis":"A deployment in the past ~1 week likely introduced a bug in the file upload or storage pipeline that truncates, re-encodes, or otherwise corrupts file data above a certain size threshold. Prime suspects: (1) a change to chunked upload handling that drops or misordering chunks for multi-part uploads, (2) a streaming/buffering change that truncates the response body at a size boundary, (3) a middleware or proxy change (e.g., body size limit, compression, or encoding) that mangles binary data beyond a threshold.","reproduction_steps":["Obtain a PDF file in the 2-5 MB range (a multi-page business report)","Upload it as an attachment to any task in TaskFlow","Download the attachment","Attempt to open the downloaded file in a PDF viewer — it should report the file as damaged","Repeat with a small PDF (<1 MB) to confirm it succeeds — this establishes the size-dependent behavior"],"environment":"Production environment. No specific OS/browser constraints reported — appears to affect all customers using the file attachment feature. Regression window: approximately the last 7 days from the date of report.","severity":"high","impact":"Multiple customers are affected daily. The file attachment feature is core to the reporter's team workflow. Customers are unable to share PDF reports through TaskFlow, causing escalations. No workaround has been identified — affected users cannot use the feature at all for larger files.","recommended_fix":"1. Review all deployments from the past 7-10 days that touch the file upload/download pipeline, storage layer, or any reverse proxy / CDN / middleware configuration. 2. Binary-diff an uploaded-then-downloaded PDF against the original to identify where corruption occurs (truncation, encoding change, byte offset, etc.). 3. Test with varying file sizes to pinpoint the exact threshold. 4. Test with non-PDF binary files to determine whether the issue is PDF-specific or affects all binary uploads above the threshold. 5. Check for recent changes to multipart upload handling, streaming buffers, Content-Type / Content-Length headers, or body size limits.","proposed_test_case":"Upload binary files at multiple size thresholds (500 KB, 1 MB, 2 MB, 5 MB, 10 MB) in both PDF and non-PDF formats. After download, perform a byte-for-byte comparison (e.g., sha256sum) between the original and downloaded file. The test passes when all downloaded files match their originals exactly.","information_gaps":["Exact file size threshold where corruption begins","Whether non-PDF binary files of similar size are also affected","Whether downloaded corrupted files are the same byte size as the originals (would distinguish truncation from encoding corruption)","Specific browser and OS the reporter is using (unlikely to be relevant given multiple customers affected)","Whether the corruption is visible in the stored blob (storage-side) or introduced during download (serving-side)"]}

---
