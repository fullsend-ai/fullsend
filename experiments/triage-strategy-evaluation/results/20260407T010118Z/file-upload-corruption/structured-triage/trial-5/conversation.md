# Conversation: file-upload-corruption x structured-triage

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted file uploads are definitely something we want to get to the bottom of quickly.

Could you walk us through the exact steps to reproduce this? For example:
- Where in the app are you uploading the PDF (task detail view, a specific form, drag-and-drop vs. file picker)?
- Roughly how large are the files that come through corrupted?
- Does it happen with every PDF, or only certain ones?

This will help us narrow down where the corruption is being introduced.

---

### reporter

Look, I don't know all the technical details about where exactly in the app it happens — our team uploads PDFs from the task detail view, usually drag and drop. It happens with a lot of our PDFs but I've noticed that some smaller ones seem to be fine? I don't know why. The point is our customers are sending us important documents and we can't open them, this has been going on for about a week now and it's really hurting us.

---

### triage-agent

Thanks for those details — that's really helpful. Knowing it's drag-and-drop from the task detail view and that smaller files seem okay gives us a solid starting point.

Could you let us know what browser and operating system your team is using? For example, Chrome on Windows, Safari on macOS, etc. If different team members use different setups, it would be useful to know whether the corruption happens across all of them or only on specific ones.

This will help us figure out whether it's a browser-specific issue or something on our server side.

---

### reporter

We've got people on Chrome, Firefox, some on Mac and some on Windows. It's happening for everyone so I don't think it's a browser thing. Can you guys just look at your servers? This started about a week ago and it was working fine before that.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted on upload via drag-and-drop — likely size-dependent","problem":"PDF files uploaded as task attachments via drag-and-drop in the task detail view are corrupted and cannot be opened. The PDF viewer reports the file is damaged. The issue affects multiple customers across different browsers and operating systems. Smaller PDFs appear to upload successfully; larger ones are consistently corrupted.","root_cause_hypothesis":"A size-dependent corruption suggests a chunked upload, streaming, or encoding issue — likely a regression introduced approximately one week ago. Possible causes include: a server-side change truncating or re-encoding the upload stream for files exceeding a buffer/chunk boundary, a misconfigured multipart upload threshold, or a broken content-transfer-encoding (e.g., binary file being processed as text/UTF-8). The one-week timeline points to a recent deployment or infrastructure change.","reproduction_steps":["Open a task in the task detail view","Drag and drop a PDF file (larger than ~1-2 MB) onto the attachment area","Wait for the upload to complete","Download the uploaded PDF","Attempt to open the downloaded PDF — it will report as damaged/corrupted"],"environment":"Cross-browser (Chrome, Firefox) and cross-platform (macOS, Windows). Not browser- or OS-specific. Regression started approximately one week ago.","severity":"high","impact":"Multiple customers are unable to exchange PDF documents through the task attachment feature. This blocks workflows that depend on document sharing and has been ongoing for approximately one week.","recommended_fix":"1. Check deployment history for changes ~1 week ago affecting file upload, storage, or streaming code. 2. Compare a corrupted uploaded file byte-for-byte against the original to identify where corruption occurs (truncation, encoding, partial write). 3. Investigate whether there is a file-size threshold (e.g., multipart chunking boundary) that triggers the corruption. 4. Check server upload handling for binary vs. text encoding issues (e.g., ensure files are streamed as application/octet-stream, not re-encoded).","proposed_test_case":"Upload PDF files of varying sizes (e.g., 100 KB, 1 MB, 5 MB, 20 MB) via drag-and-drop in the task detail view. Download each and verify the downloaded file is byte-identical to the original (checksum comparison). The test should confirm that files above and below the corruption threshold all survive the round trip intact.","information_gaps":["Exact file size threshold where corruption begins","Whether the corruption is truncation, encoding mangling, or something else (requires server-side binary comparison)","Specific deployment or infrastructure change that coincided with the onset ~1 week ago"]}

---
