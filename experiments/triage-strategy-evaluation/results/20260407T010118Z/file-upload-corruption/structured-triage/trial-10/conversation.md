# Conversation: file-upload-corruption x structured-triage

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted file uploads are definitely something we want to get to the bottom of quickly.

Could you walk me through the exact steps you're taking when uploading a PDF? For example:
- Where in the app are you uploading (which screen/feature)?
- How are you uploading (drag-and-drop, file picker button, etc.)?
- What happens after you upload — does it appear to succeed, or do you see any error at that point?
- When you try to open/download the file afterward, what exactly does the PDF viewer say?

This will help us reproduce the issue on our end.

---

### reporter

Look, I don't remember every little detail about how we upload. We just use the normal attachment button on the task page, click it, pick a PDF, it uploads. It looks like it works fine — no errors or anything. Then when someone tries to download it later, the PDF is just broken. Adobe says it's damaged, can't open it. This is affecting our whole team and we need it fixed, not a step-by-step walkthrough of how to click a button.

---

### triage-agent

Completely understand the frustration — this sounds like it's blocking real work and we want to get it fixed.

One thing that would help us narrow this down quickly: what browser and operating system is your team using? And if you happen to know what version of TaskFlow you're on (usually visible in Settings → About), that would help too.

This matters because file upload corruption sometimes traces back to how a specific browser handles the transfer, and knowing the app version tells us whether this might be a recent regression we can pinpoint fast.

---

### reporter

We're on TaskFlow v2.3.1. Most of us use Chrome but it happens regardless of browser — I've had people try Firefox too and same thing. We're on Windows 10 mostly. But I really don't think it's a browser thing because the upload looks totally fine, no errors, it's only when you download the file later that it's corrupted.

---

### triage-agent

That's really helpful context — thank you. One more thing that will directly affect where our developers look for the fix:

Does this happen with **every** PDF you upload, or only some? For example, have you noticed any pattern around file size (small vs. large PDFs) or where the PDF came from (scanned documents vs. generated reports, etc.)?

Also — are **other file types** (images, Word docs, etc.) also coming through corrupted, or is it just PDFs?

This tells us whether the problem is specific to how we handle PDFs or something broader in the file upload pipeline.

---

### reporter

I don't know, maybe not every single one? I feel like some smaller ones might have been okay but I'm not totally sure. We upload a lot of PDFs so it's hard to keep track. As for other file types — honestly we mostly deal with PDFs so I haven't paid much attention. Maybe some images looked a little off but I figured that was something else. Can you guys just look into the upload system? Something clearly broke recently because this wasn't happening before.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted on download after upload via task page (v2.3.1)","problem":"PDF files uploaded through the task page attachment button appear to upload successfully (no client-side errors), but when subsequently downloaded, the files are corrupted and cannot be opened. Adobe Reader reports the file is damaged. The issue affects multiple users across the reporter's team and is browser-independent.","root_cause_hypothesis":"The corruption occurs server-side during upload processing or storage, not during the browser upload itself (no client errors, reproduces across Chrome and Firefox). Likely candidates: (1) a encoding/transformation issue in the upload pipeline (e.g., binary file being processed as text, incorrect Content-Type handling, or a middleware stripping/modifying binary data), (2) a storage layer issue truncating or corrupting the file bytes, or (3) a regression introduced before v2.3.1 in the file handling code. The reporter's observation that smaller files may be unaffected suggests possible chunked upload or streaming issues with larger payloads.","reproduction_steps":["Open a task in TaskFlow v2.3.1","Click the attachment button on the task page","Select a PDF file (try one >1 MB) via the file picker","Upload completes with no visible errors","Download the uploaded PDF from the task","Attempt to open the downloaded PDF — expect Adobe Reader to report the file is damaged"],"environment":"TaskFlow v2.3.1, Windows 10, Chrome and Firefox (browser-independent), standard task page file attachment feature","severity":"high","impact":"Multiple users on the same team are unable to use PDF attachments, which is a core workflow for them. File attachments are effectively broken for their primary use case (PDFs). The reporter indicates this is a recent regression.","recommended_fix":"Compare the byte content of a PDF before upload and after download to identify where corruption occurs (truncation, encoding change, byte insertion). Check the upload endpoint for any middleware that processes request bodies as text rather than binary streams. Inspect Content-Type and transfer-encoding headers. Review recent changes to the file upload/storage/download pipeline around the v2.3.1 release for regressions. Test with varying file sizes to confirm the size-correlation hypothesis.","proposed_test_case":"Upload a PDF of known size and checksum via the task page attachment button, then download it and verify the checksum matches. Repeat with multiple file sizes (e.g., 100 KB, 1 MB, 10 MB) to identify a corruption threshold. Also test with non-PDF binary files (e.g., DOCX, PNG) to determine if the issue is PDF-specific or affects all binary uploads.","information_gaps":["Exact file size threshold at which corruption begins (reporter unsure)","Whether non-PDF file types are also affected (reporter mostly uses PDFs, uncertain about images)","Exact date or version when the issue started (reporter says 'recently' but no specific version or date)","Server-side logs or error output during upload/download (not available from reporter)"]}

---
