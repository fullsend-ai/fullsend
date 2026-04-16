# Conversation: file-upload-corruption x structured-triage

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted file uploads are definitely something we want to get to the bottom of quickly.

Could you walk me through the exact steps you're taking when uploading a PDF? Specifically:
- Where in the app are you uploading (e.g., task detail view, a specific form)?
- How are you uploading (drag-and-drop, the upload button, API)?
- Does this happen with **every** PDF you upload, or only certain ones (e.g., larger files, scanned documents)?

If you're able to test with a small, simple PDF (like a one-page text document), that would also help us narrow down whether file size or content plays a role.

---

### reporter

Look, we upload PDFs through the task detail view, using the upload button. It's not just me — multiple people on my team are hitting this. I don't have time to run tests for you, but I can tell you that some smaller PDFs seem to work fine? The ones that are broken tend to be the bigger ones — customer contracts, reports, that kind of thing. Can you just look into what changed recently? This wasn't happening before.

---

### triage-agent

Thanks for those details — that's really helpful. The fact that it's mainly larger files (contracts, reports) while smaller PDFs come through fine is a strong clue.

One thing that would help us narrow this down: could you share what browser and operating system you (or your team members) are using, and which version of TaskFlow you're on? You can find the version in **Settings → About**.

We're asking because larger file uploads can be handled differently depending on the browser, and knowing the app version will help us check what changed recently — which is exactly what we want to investigate.

---

### reporter

I don't know what browser everyone's using — Chrome, probably? We're on TaskFlow 2.3.1. Look, I don't think this is a browser thing. It was working fine until recently and then it just stopped. Something changed on your end.

---

### triage-agent

[RESOLVED] {"title":"Large PDF attachments are corrupted after upload via task detail view","problem":"PDF files uploaded through the task detail view upload button are corrupted and cannot be opened. The issue disproportionately affects larger files (contracts, reports) while smaller/simpler PDFs upload successfully. Multiple users on the same team are affected. The reporter states this is a regression — it was working previously.","root_cause_hypothesis":"A recent change (at or before v2.3.1) likely introduced a bug in how large file uploads are handled — possible causes include incorrect chunked upload reassembly, a file size limit silently truncating the upload, a timeout during upload of larger files, or a content-type/encoding issue in the upload pipeline that only manifests above a certain file size.","reproduction_steps":["Log into TaskFlow 2.3.1","Navigate to any task's detail view","Click the upload button and select a larger PDF (e.g., a multi-page contract or report, likely >5-10MB)","Wait for upload to complete","Attempt to open/download the uploaded PDF","Observe that the PDF viewer reports the file as damaged or corrupted","Repeat with a small, simple PDF (e.g., one-page text document) and observe it works correctly"],"environment":"TaskFlow 2.3.1, Chrome (likely), multiple users affected across one team","severity":"high","impact":"Multiple customers are unable to attach larger PDF documents (contracts, reports) to tasks, directly impacting daily workflow for teams that rely heavily on file attachments.","recommended_fix":"1. Check git history for recent changes to the file upload pipeline, attachment storage, or related middleware (especially anything touching chunked uploads, streaming, or size limits). 2. Compare the uploaded file's byte size and checksum against the original to determine if truncation or corruption is occurring during upload vs. storage vs. retrieval. 3. Test uploads at various file sizes to identify the threshold where corruption begins. 4. Check server-side logs for any errors or warnings during large file uploads (e.g., request body size limits, proxy timeouts).","proposed_test_case":"Upload PDFs at multiple sizes (1MB, 5MB, 10MB, 25MB, 50MB) and verify that each downloaded file has an identical checksum to the original. Add a regression test that uploads a file above the identified corruption threshold and asserts byte-for-byte integrity.","information_gaps":["Exact file size threshold where corruption begins","Whether the corruption is truncation, encoding mangling, or something else (comparing file sizes of original vs. uploaded would clarify)","Exact browsers and OS versions across affected team members","Whether the issue reproduces with non-PDF large files (images, documents)"]}

---
