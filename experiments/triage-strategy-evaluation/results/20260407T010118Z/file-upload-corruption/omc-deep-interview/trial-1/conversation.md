# Conversation: file-upload-corruption x omc-deep-interview

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how disruptive this would be given how much you rely on file attachments.

To help us narrow this down: could you walk us through the exact steps you're taking when uploading a PDF? Specifically:

- Where in TaskFlow are you uploading (e.g., task detail view, a specific form)?
- Roughly how large are the PDFs that come through corrupted?
- Does this happen with *every* PDF, or only some? If only some, is there anything the broken ones have in common (size, source application, etc.)?

Also, if you download one of the corrupted files and compare its file size to the original, are they the same size or different?

---

### reporter

Look, I don't know all the technical details, I just know our PDFs are broken and my team is struggling. We upload them from the task detail view, the little paperclip icon. It happens with a lot of our PDFs — these are customer contracts and reports so they're not tiny files. I did notice that some smaller PDFs seem to work fine actually, but the bigger ones are the problem. I haven't compared file sizes, I just know they won't open. This started maybe a week ago? Can you guys just fix it?

---

### triage-agent

Totally understand — we want to get this fixed for you as quickly as possible.

The detail about smaller PDFs working but larger ones breaking is really helpful. Do you have a rough sense of the size cutoff? For example, do PDFs under a certain size (say 5 MB, 10 MB) tend to work, while ones above that break? Even a ballpark would help our developers zero in on the issue.

Also, one quick check: did anything change in your workflow or environment around a week ago when this started — like a browser update, a switch to a new browser, or a change in how the PDFs are generated?

---

### reporter

I don't know the exact size cutoff, I'm not sitting here measuring file sizes. I just know the little one-page PDFs seem to open fine and the bigger contracts that are multiple pages don't. Nothing changed on our end — same browser, same everything. Whatever you guys changed on your end broke it.

---

### triage-agent

Completely understand — and you're right, something likely changed on our side. One last thing that would really help our developers jump straight to the fix:

When you try to open one of the broken PDFs, what exactly do you see? For example:
- Does the PDF viewer show a completely blank page?
- Does it show the first page or two correctly and then go blank?
- Does it refuse to open entirely with an error message?

This will tell us whether the file is getting cut off partway through or scrambled entirely, which points us at very different parts of the system.

---

### reporter

It just says the file is damaged or corrupted and won't open at all. Sometimes I get a bunch of garbled text if I try to force it open. It's not like half the document shows up — the whole thing is just trashed. Are you guys close to figuring this out? My team has deadlines.

---

### triage-agent

[RESOLVED] {"title":"Large PDF attachments uploaded via task detail view are corrupted/unreadable on download","problem":"PDF files uploaded through the task detail view paperclip icon are completely corrupted when downloaded. The files cannot be opened — PDF viewers report them as damaged, and forcing them open shows garbled text. The issue is size-dependent: small single-page PDFs work fine, but larger multi-page documents (customer contracts, reports) are consistently broken. Multiple customers are affected. The issue began approximately one week ago with no changes on the client side.","root_cause_hypothesis":"A recent server-side change (likely deployed ~1 week ago) is corrupting larger file uploads. The fact that files are fully garbled (not truncated) and the issue is size-dependent suggests either: (1) a chunked upload/transfer encoding issue where reassembly is broken for multi-chunk files, (2) a content-type or binary encoding error applied during upload processing that only manifests above a single-chunk threshold, or (3) a middleware or proxy change (e.g., request body size limit, compression, or encoding transformation) that mangles payloads above a certain size.","reproduction_steps":["Prepare two PDFs: one small single-page file (<1 MB) and one larger multi-page file (e.g., 5+ MB contract)","Navigate to any task's detail view in TaskFlow","Click the paperclip icon to upload the small PDF","Download the uploaded small PDF and verify it opens correctly (expected: works fine)","Repeat upload with the larger multi-page PDF","Download the uploaded large PDF and attempt to open it (expected: corrupted/damaged error)","Examine the downloaded file in a hex editor — compare headers and size to the original to determine if the file is garbled, truncated, or has incorrect encoding"],"environment":"TaskFlow web application, task detail view file attachment feature. Browser and OS not specific to the issue (no client-side changes). Issue is server-side and affects multiple customers.","severity":"high","impact":"Multiple customers are unable to upload and share large PDF documents (contracts, reports) that are core to their workflow. Teams are blocked on deadline-sensitive work. The file attachment feature is heavily used, making this a broad-impact regression.","recommended_fix":"1. Check deployment history for changes made ~1 week ago to the file upload pipeline (upload endpoint, storage service, middleware, reverse proxy config). 2. Compare the raw bytes of an uploaded large file in storage against the original — determine whether corruption happens during upload, storage, or download. 3. Investigate chunked transfer encoding or multipart upload handling for files above the single-chunk threshold. 4. Check for recently added middleware that might transform request/response bodies (compression, encoding, content-type normalization). 5. Verify that the upload handler preserves binary content-type (application/octet-stream or application/pdf) and doesn't apply text encoding transformations.","proposed_test_case":"Upload PDFs of varying sizes (1 MB, 5 MB, 10 MB, 25 MB) through the task detail attachment feature. Download each and perform a byte-for-byte comparison against the original using checksums (SHA-256). All files should match exactly. Additionally, add an automated integration test that uploads a binary file above the chunking threshold and verifies the downloaded file's checksum matches the original.","information_gaps":["Exact file size threshold where corruption begins (reporter couldn't specify, but developers can determine via testing)","Whether the corruption also affects non-PDF binary files (images, Word docs) or is PDF-specific","Exact deployment or change that occurred ~1 week ago that may have introduced the regression"]}

---
