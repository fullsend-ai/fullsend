# Conversation: file-upload-corruption x superpowers-brainstorming

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — that sounds really disruptive. To help us narrow down what's going on, can you tell us:

Is this happening with **every** PDF you upload, or only certain ones?

1. Every PDF, regardless of size or source
2. Only large PDFs (e.g., over a certain file size)
3. Only PDFs from a specific source (e.g., scanned documents, exported from a particular app)
4. It seems random — some work, some don't
5. Something else

---

### reporter

I don't know, it seems kind of random honestly. Some PDFs work fine and some don't. I haven't really tracked which ones exactly. All I know is customers are complaining and we need this fixed. It's been going on for about a week now.

---

### triage-agent

Thanks for the extra detail — that helps. One key thing that would point us in the right direction:

When you upload a PDF that ends up corrupted, is it broken **immediately** when you try to download/view it, or did it work at first and break later?

1. It's broken right away — upload it, try to open it, it's damaged
2. It worked initially but stopped opening correctly at some point after
3. I'm not sure — we usually don't check right after uploading

Also, did anything change about a week ago when this started — a software update, a change in how files are uploaded, or anything else you noticed?

---

### reporter

It's broken right away — you upload it and immediately it's garbage when you try to open it. And no, nothing changed on our end. We didn't do anything different. Maybe you guys pushed an update or something? I don't know, but it was working fine before and now it's not. Can you just look into it?

---

### triage-agent

[RESOLVED] {"title":"PDF attachments silently corrupted during upload (intermittent, regression ~1 week ago)","problem":"PDF files uploaded to task attachments are sometimes corrupted and unreadable immediately after upload. The corruption is intermittent — some PDFs upload fine while others are damaged. Multiple customers are affected. The issue began approximately one week ago with no changes on the client side, indicating a server-side regression.","root_cause_hypothesis":"A server-side change deployed ~1 week ago likely introduced a bug in the file upload pipeline. The intermittent nature combined with immediate corruption suggests either: (1) a multipart/chunked upload issue where large or certain-sized files are truncated or reassembled incorrectly, (2) an encoding/content-type handling change that corrupts binary data (e.g., treating binary as UTF-8), or (3) a race condition in the storage layer that sometimes writes incomplete data.","reproduction_steps":["Prepare several PDF files of varying sizes (small <100KB, medium 1-5MB, large >10MB) and sources","Upload each PDF as a task attachment","Immediately attempt to download/view each uploaded PDF","Compare file sizes and checksums of the original vs downloaded files to identify truncation or byte corruption","Note which files are corrupted — look for a pattern in file size or content"],"environment":"Server-side issue affecting all customers using the file attachment feature. No specific client OS/browser correlation identified — likely independent of client environment.","severity":"high","impact":"Multiple customers are unable to reliably use the file attachment feature for PDFs, which is described as heavily used. This is blocking core workflows and has persisted for approximately one week.","recommended_fix":"1. Review all server-side deployments from ~1 week ago, focusing on changes to the file upload pipeline, storage layer, API gateway, or any middleware handling multipart uploads. 2. Compare a corrupted uploaded file against its original — check for truncation (file size mismatch), byte-level corruption, or encoding issues. 3. Check if the upload handler correctly preserves binary content-type (application/octet-stream or application/pdf) without any text encoding transformation. 4. If using chunked uploads, verify chunk reassembly logic. 5. Check for any proxy or CDN configuration changes that might alter request bodies.","proposed_test_case":"Upload PDFs of various sizes (1KB, 100KB, 1MB, 10MB, 50MB) and verify that the downloaded file is byte-identical to the original (matching SHA-256 checksum). Include both single-page and multi-page PDFs. This test should run against the upload pipeline in CI to catch future regressions.","information_gaps":["Exact file size threshold or pattern distinguishing corrupted from successful uploads (developer can determine during reproduction)","Whether non-PDF file types are also affected (would widen the scope if so)","The specific server-side deployment or change that introduced the regression (requires deployment log review)"]}

---
