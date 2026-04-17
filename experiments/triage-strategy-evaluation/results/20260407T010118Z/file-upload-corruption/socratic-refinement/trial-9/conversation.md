# Conversation: file-upload-corruption x socratic-refinement

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments sound really frustrating, especially if your team relies on them heavily.

To help me understand the full picture: can you walk me through what you were doing when you first noticed this? For instance, were you uploading a specific kind of PDF (scanned documents, generated reports, forms), and did this start happening recently or has it been an ongoing issue? If it did start recently, do you recall anything that changed around that time — a software update, a new workflow, different types of files?

---

### reporter

Look, it started maybe a week ago? We upload PDFs all the time — contracts, reports, scanned docs, you name it. They were all working fine before and now they're just broken. I don't know what changed on your end but something did. Can you just look into it? Our customers are waiting on these files.

---

### triage-agent

I completely understand the urgency — let's figure this out together.

When you download one of these corrupted PDFs back from TaskFlow, have you noticed anything about the file itself? For example, is the downloaded file roughly the same size as the original you uploaded, or does it seem smaller (or larger)? And when the PDF viewer says the file is damaged, does it fail to open entirely, or does it partially render with missing pages or garbled content?

Also, if you upload one of these PDFs and then immediately download it back, is it already broken at that point — or does the corruption seem to happen later?

---

### reporter

I don't know the exact file sizes, I'm not sitting here comparing bytes. When you try to open them the viewer just says the file is damaged and won't open at all. And yeah it's broken right away — upload it, download it back, it's already corrupted. I will say though, I've noticed some of our smaller PDFs seem to come through okay? Like quick one-pagers seem fine but the bigger contracts and reports are the ones that are messed up. I don't know if that helps but can someone please just fix this?

---

### triage-agent

That detail about smaller PDFs working is actually really helpful — it narrows things down a lot. It sounds like something is going wrong specifically with how larger files are being processed.

Two quick things that would help us zero in on this: roughly how big are the PDFs that come through broken — are we talking 5–10 MB range, or more like 50+ MB? And are you uploading these through the TaskFlow web interface, or through another method like the API or a mobile app?

I know you want this fixed fast and I appreciate your patience — this size clue is a strong lead.

---

### reporter

I don't know exactly how big they are, they're just normal business documents. The contracts are usually a few pages, the reports can be longer. I'm not measuring file sizes. We use the web interface for everything. Can you just check what changed on your end about a week ago? Something broke and it wasn't us.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted on upload/download — size-dependent regression (~1 week)","problem":"PDF files uploaded to tasks via the web interface are returned corrupted when downloaded. The PDF viewer reports the file as damaged and will not open it at all (no partial rendering). The issue is size-dependent: small single-page PDFs appear unaffected, while larger multi-page documents (contracts, reports) are consistently broken. The corruption is immediate — uploading and immediately re-downloading reproduces it.","root_cause_hypothesis":"A deployment or configuration change approximately one week ago likely introduced a bug in the file upload/storage pipeline that affects files above a certain size threshold. Probable causes include: (1) a chunked upload or multipart reassembly bug that drops or misordering chunks for files exceeding a buffer/chunk boundary, (2) a misconfigured reverse proxy, CDN, or middleware that truncates or re-encodes request bodies beyond a size limit, (3) a binary-vs-text encoding regression (e.g., base64 or transfer-encoding change) that only manifests when the payload crosses a certain size, or (4) a streaming/buffering change in the storage layer that silently corrupts larger writes.","reproduction_steps":["Prepare two PDF files: one small (~1 page, under 500KB) and one larger (multi-page contract or report, likely several MB)","Upload the small PDF to a task via the TaskFlow web interface","Immediately download the small PDF and verify it opens correctly","Upload the larger PDF to a task via the TaskFlow web interface","Immediately download the larger PDF — expect it to be reported as damaged/corrupted by a PDF viewer","Compare the byte sizes of the original and downloaded larger PDF to confirm truncation or modification"],"environment":"TaskFlow web interface. Multiple customers affected. No specific browser or OS identified, but the immediate corruption on re-download indicates a server-side issue independent of client environment.","severity":"high","impact":"Multiple customers are unable to use the file attachment feature for standard business documents (contracts, reports). This blocks workflows that depend on sharing PDFs through TaskFlow. The reporter describes it as heavily impacting their workflow with customers waiting on files.","recommended_fix":"1. Review all deployments and configuration changes from approximately one week ago, focusing on the file upload endpoint, storage service, reverse proxy/load balancer, and any middleware in the upload path. 2. Binary-diff an uploaded-then-downloaded large PDF against its original to determine the nature of the corruption (truncation, encoding change, chunk misordering, or byte modification). 3. Identify the exact size threshold by testing uploads at various sizes (e.g., 500KB, 1MB, 2MB, 5MB, 10MB). 4. Check for recent changes to multipart upload handling, request body size limits, streaming/buffering configuration, or content-type handling.","proposed_test_case":"Upload PDF files at a range of sizes (100KB, 500KB, 1MB, 5MB, 10MB, 25MB) via the web interface, immediately download each, and assert byte-for-byte equality between the original and downloaded file. This should be an automated integration test against the upload/download pipeline.","information_gaps":["Exact file size threshold where corruption begins (developer can determine empirically)","Nature of the corruption — truncation vs. encoding vs. byte modification (developer can determine by binary diff)","Specific deployment or config change from ~1 week ago that triggered the regression (requires internal deploy history review)"]}

---
