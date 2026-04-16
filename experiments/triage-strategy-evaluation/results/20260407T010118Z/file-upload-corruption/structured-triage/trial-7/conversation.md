# Conversation: file-upload-corruption x structured-triage

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments definitely need to be addressed quickly.

To help us investigate, could you walk us through the exact steps you're taking when this happens? For example:

1. Where in the app are you uploading the PDF? (task detail page, a specific form, drag-and-drop vs. file picker, etc.)
2. How are you then trying to open/download the file afterward?
3. Does this happen with all PDFs or only certain ones (e.g., large files, scanned documents)?

This will help us reproduce the issue on our end.

---

### reporter

Look, I don't remember every little detail about how I'm uploading them. I just click the attachment button on the task, pick the PDF, and it uploads. Then when anyone tries to download it, it's corrupted. I've tried both drag-and-drop and the file picker, same result. As for which PDFs — I don't know, it seems like the bigger ones? Our smaller internal PDFs seem fine actually, but the ones customers send us which tend to be larger reports are all coming through broken. I just need this fixed, our team is dealing with angry customers every day because of this.

---

### triage-agent

Thanks for those details — the size correlation is a really useful clue and will help us narrow this down.

Could you let us know what environment you're working in? Specifically:

- **Browser and version** (e.g., Chrome 124, Firefox 126)
- **Operating system** (e.g., Windows 11, macOS Sonoma)
- **TaskFlow version** if you know it (visible in Settings → About)

This helps us check whether there are any known platform-specific issues with large file uploads.

---

### reporter

I don't know what browser version, Chrome probably? Whatever the latest is. Windows 10 I think. TaskFlow version... I'd have to look that up, I don't have it in front of me. Can you just look into the file size thing? That seems like the actual problem here, not what browser I'm using.

---

### triage-agent

[RESOLVED] {"title":"Large PDF attachments are corrupted after upload — likely file size related","problem":"PDF files uploaded as task attachments become corrupted and cannot be opened after download. The PDF viewer reports the file as damaged. This affects multiple customers and is causing daily workflow disruption. The issue appears to be correlated with file size — larger PDFs (e.g., customer reports) are consistently corrupted, while smaller internal PDFs upload and download fine.","root_cause_hypothesis":"Large file uploads are being truncated or incorrectly processed, likely due to a file size limit in the upload pipeline (e.g., request body size limit in the web server, API gateway, or multipart upload handler), or a chunked transfer encoding issue where large files are not being fully reassembled before storage.","reproduction_steps":["Open a task detail page in TaskFlow","Click the attachment button (or use drag-and-drop)","Upload a large PDF file (e.g., a multi-page customer report — try files over a few MB)","Wait for upload to complete","Download the uploaded PDF","Attempt to open the downloaded PDF — it will report as damaged/corrupted"],"environment":"Chrome (latest) on Windows 10. TaskFlow version unknown. Issue reported by multiple customers, suggesting it is not environment-specific.","severity":"high","impact":"Multiple customers are affected daily. Teams relying on file attachments for customer-facing workflows cannot share PDF reports, causing direct customer-facing impact and angry customers.","recommended_fix":"1. Compare uploaded vs. downloaded file sizes for a corrupted PDF to confirm truncation. 2. Check web server and reverse proxy configs for request body size limits (e.g., nginx client_max_body_size, API gateway payload limits). 3. Inspect the upload handler for chunked/multipart transfer issues with large files. 4. Check if the storage layer (S3, local disk, etc.) is receiving the complete file. 5. Verify Content-Length and Content-Type headers are correct on both upload and download paths.","proposed_test_case":"Upload PDF files of increasing sizes (e.g., 1MB, 5MB, 10MB, 25MB, 50MB) and verify that each downloaded file is byte-identical to the original by comparing checksums (SHA-256). Also test with both drag-and-drop and file picker upload methods.","information_gaps":["Exact file size threshold where corruption begins","Browser and TaskFlow version details (reporter declined to provide)","Whether the issue occurs in other browsers or operating systems","Server-side error logs during the upload of affected files"]}

---
