# Conversation: file-upload-corruption x omc-deep-interview

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted uploads are definitely something we want to get to the bottom of quickly.

To help us narrow down the cause: does this happen with **all** PDF uploads, or only certain files (e.g., larger files, scanned documents, PDFs with specific features)? Also, when you download the corrupted file back, is the file size roughly the same as the original, or does it look significantly smaller or larger? Any specific error message from the PDF viewer would be helpful too.

---

### reporter

Look, I don't have time to go through every single PDF and check file sizes. All I know is our team uploads PDFs constantly and a bunch of them are coming through broken. I think the smaller ones might be okay actually — like quick one-pagers seem fine? But the bigger reports and contracts are definitely messed up. The error just says the file is damaged or corrupted, nothing more specific than that. This started like a week ago and it's really causing problems for us.

---

### triage-agent

That's really helpful context — thank you. The pattern with larger files being affected but smaller one-pagers working fine is a strong clue.

To help our developers reproduce this: roughly how large are the PDFs that come through broken? (e.g., are we talking 5 MB+, 10 MB+, or something else?) And are your team uploading these through the web interface in a browser, or through our API/integrations? If it's the browser, which one (Chrome, Firefox, Safari, etc.)?

---

### reporter

I don't know the exact sizes, I'm not sitting here measuring every file. They're like... normal business documents, contracts, reports with graphics. Probably a few MB? We use the web interface, Chrome mostly. Can you guys just fix this? It worked fine before last week.

---

### triage-agent

Totally understand the urgency — we're taking this seriously.

One quick question that will really help our developers zero in on the right fix: does your team upload other file types besides PDFs (like Word docs, images, or spreadsheets)? If so, are those coming through fine, or are you seeing the same corruption with non-PDF files too?

This will tell us whether the issue is in how we handle PDFs specifically or in the upload pipeline itself — two very different fixes.

---

### reporter

Honestly I'm not sure, we mostly deal with PDFs. I think someone mentioned a big image came through looking weird the other day but I didn't look into it. PDFs are the main problem for us right now.

---

### triage-agent

Completely understand — we're on it.

One last thing that will point our developers straight at the fix: when you open one of the broken PDFs after downloading it back from TaskFlow, does it look like the file is **cut short** (e.g., first page or two render fine but the rest is missing), or is the **whole file unreadable** from the very start?

This tells us whether the file is getting truncated during upload or corrupted in a different way — two very different fixes on our end.

---

### reporter

The whole thing is busted. You can't see anything at all, it's not like half the document shows up. It's just completely unreadable. Are you guys actually working on fixing this or just asking me questions all day?

---

### triage-agent

[RESOLVED] {"title":"File uploads corrupted for larger files (PDFs, possibly images) since ~1 week ago","problem":"PDF files uploaded to tasks via the web interface are completely unreadable when downloaded. The corruption affects larger files (multi-page contracts, reports with graphics) while small single-page PDFs appear unaffected. At least one large image upload was also reported as looking wrong. The issue started approximately one week ago and affects multiple customers.","root_cause_hypothesis":"A recent change (~1 week ago) in the file upload pipeline is corrupting files above a certain size threshold. Since files are entirely unreadable (not truncated), this points to an encoding, content-type handling, or multipart reassembly issue rather than simple truncation. The size-dependent nature suggests chunked upload processing or a middleware change that mishandles larger payloads. Check deployments and infrastructure changes from ~1 week ago — likely a backend change to upload handling, a proxy/CDN configuration change, or a storage layer update.","reproduction_steps":["Log into TaskFlow web interface using Chrome","Navigate to any task and use the file attachment feature","Upload a multi-page PDF file (a few MB — e.g., a 5+ page contract or report with graphics)","After upload completes, download the file back from the task","Attempt to open the downloaded PDF — it should be completely unreadable/corrupted","Repeat with a small single-page PDF to confirm it uploads and downloads correctly"],"environment":"Web interface, Chrome browser. Issue is not browser-specific as far as we know (only Chrome confirmed). Affects production environment for multiple customers.","severity":"high","impact":"Multiple customers are unable to use file attachments for larger documents, which is a core workflow. This has been ongoing for approximately one week. Teams that rely heavily on PDF attachments for contracts and reports are significantly impacted in their daily operations.","recommended_fix":"1. Review all deployments and infrastructure changes from ~1 week ago affecting the upload pipeline, file storage, or any reverse proxy/CDN in the upload path. 2. Compare the binary content of an uploaded file in storage against the original — check for encoding corruption (e.g., base64 double-encoding, charset mangling, or incorrect Content-Type handling). 3. Check if chunked transfer encoding or multipart upload reassembly was changed. 4. Test with files of varying sizes to identify the exact size threshold where corruption begins. 5. Verify the storage layer (S3, GCS, etc.) is receiving and returning files with correct content-type and no transformation.","proposed_test_case":"Upload files of increasing sizes (100KB, 500KB, 1MB, 2MB, 5MB, 10MB) in both PDF and image formats. Download each and perform a byte-for-byte comparison (checksum) against the original. This identifies the exact size threshold and confirms whether the issue is format-specific or general. Add an automated integration test that uploads a file above the threshold and asserts the downloaded file's checksum matches the original.","information_gaps":["Exact file size threshold where corruption begins","Whether non-Chrome browsers are also affected","Whether API/integration uploads (not just web UI) are also affected","The specific deployment or infrastructure change from ~1 week ago that caused this","Whether the file is already corrupted in storage or only corrupted on download"]}

---
