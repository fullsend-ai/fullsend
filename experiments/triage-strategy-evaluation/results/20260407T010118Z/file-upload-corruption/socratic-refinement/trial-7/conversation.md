# Conversation: file-upload-corruption x socratic-refinement

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments sound really frustrating, especially if your team relies on them daily.

To help me understand the full picture: can you walk me through what you're doing when this happens? For example, what kind of PDFs are these (scanned documents, generated reports, forms?), roughly how large are they, and how are you uploading them (drag-and-drop, the attach button, API)? Also, has this always been a problem, or did it start recently — and if recently, did anything change around that time (a new browser, an update, a workflow change)?

---

### reporter

Look, they're all kinds of PDFs — contracts, reports, scanned docs, you name it. I don't know the exact sizes, they're just normal PDF files. We upload them through the regular attach button in the UI. And yes, this started recently, maybe a week or so ago? Nothing changed on our end. Can you guys just look into it? Our customers are complaining and we can't do our jobs.

---

### triage-agent

Totally understand the urgency — we want to get this sorted out quickly.

One thing that would really help narrow this down: when you upload a PDF and then immediately download it back, is the downloaded copy the one that's broken — or does it look fine at first and only appear corrupted later? And is it *every* PDF that comes through broken, or are some making it through okay?

If you could try uploading a small, simple PDF (even a one-page test doc) and letting me know whether that one also comes through damaged, that would tell us a lot about where the problem is happening.

---

### reporter

It's broken right away when you download it. And actually now that you mention it... I think some of the smaller PDFs might be getting through okay? But the ones our customers are sending us — contracts, reports, the important stuff — those are all coming through corrupted. I don't have time to sit here doing test uploads, we need this fixed. This worked fine like two weeks ago.

---

### triage-agent

That detail about smaller PDFs getting through is actually a really important clue — it points us toward where in the pipeline things are breaking.

Two things that would help us zero in on this: do you have a rough sense of what "smaller" versus "larger" means here — like, are the ones that work under a page or two, while the broken ones are 10+ pages? And when you upload other types of files (images, Word docs, spreadsheets), do those come through fine, or are you seeing the same kind of corruption there too?

That'll tell us whether this is a size-related upload issue or something specific to how we're handling PDFs.

---

### reporter

I don't know exact sizes, I'm not measuring files here. The ones that work are like... short little one-pagers I guess. The broken ones are the real documents — multi-page contracts, lengthy reports, that kind of thing. As for other file types, I mostly deal with PDFs so I can't really say. Actually, I think someone on my team mentioned a spreadsheet looking weird too but I'm not sure if that's related. Can you just check what changed on your end in the last week or two? Something clearly broke.

---

### triage-agent

[RESOLVED] {"title":"File attachments corrupted on upload/download — size-dependent, regression from ~2 weeks ago","problem":"PDF file attachments uploaded via the UI attach button are corrupted when downloaded. The corruption is immediate — files are broken on the first download attempt. The issue correlates with file size: small single-page PDFs appear to upload successfully, while larger multi-page documents (contracts, reports, scanned documents) are consistently corrupted. There is an unconfirmed report that a spreadsheet was also affected, suggesting the issue may not be PDF-specific.","root_cause_hypothesis":"A change deployed approximately 1–2 weeks ago likely introduced a regression in the file upload pipeline that truncates, re-encodes, or otherwise corrupts files above a certain size threshold. Possible causes include: a new file size limit or chunked upload change that silently truncates large files, a middleware or proxy change (e.g., request body size limit), a binary-to-text encoding error (e.g., base64 or multipart boundary handling) that only manifests on larger payloads, or a storage layer change that corrupts writes above a certain size.","reproduction_steps":["Prepare a multi-page PDF file (e.g., a 5–10 page contract or report)","Log into TaskFlow and navigate to any task","Click the attach button in the UI and upload the PDF","Immediately download the uploaded attachment","Attempt to open the downloaded file — it should be reported as damaged/corrupted","Repeat with a simple one-page PDF — this one should open successfully","Compare the file sizes of the original and downloaded copies to check for truncation"],"environment":"TaskFlow web UI, attach button upload flow. No specific browser/OS reported — issue appears server-side given the size-dependent pattern. Production environment, affecting multiple customers.","severity":"high","impact":"Multiple customers are unable to use file attachments for standard business documents (contracts, reports). The team relies heavily on this feature and their workflow is blocked. Only trivially small files are getting through.","recommended_fix":"1. Review all commits/deployments from the past 2 weeks that touched the file upload pipeline, storage layer, or any proxy/middleware configuration. 2. Compare the byte content of an uploaded-then-downloaded file against the original to determine the nature of the corruption (truncation, encoding mangling, partial write). 3. Check for recently introduced file size limits, request body limits (e.g., nginx client_max_body_size, API gateway limits), or changes to multipart form handling. 4. Check if chunked upload or streaming logic was modified. 5. Test with non-PDF files of similar size to confirm whether the issue is size-dependent or format-dependent.","proposed_test_case":"Upload files of increasing sizes (100KB, 500KB, 1MB, 5MB, 10MB) in multiple formats (PDF, XLSX, DOCX, PNG) via the UI attach button. Download each immediately and verify byte-for-byte integrity against the original using checksums. The test should identify the exact size threshold at which corruption begins and whether it is format-dependent.","information_gaps":["Exact file size threshold where corruption begins (developer can determine via systematic testing)","Whether non-PDF file types are equally affected (one unconfirmed report of a corrupt spreadsheet)","Nature of the corruption — truncation vs. encoding error vs. something else (developer can determine by comparing original and downloaded bytes)","Specific browser and OS versions in use (likely irrelevant given the server-side, size-dependent pattern)"]}

---
