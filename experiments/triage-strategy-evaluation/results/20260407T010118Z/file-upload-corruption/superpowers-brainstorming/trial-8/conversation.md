# Conversation: file-upload-corruption x superpowers-brainstorming

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments blocking your workflow is definitely something we want to get to the bottom of quickly.

To help narrow this down: is this happening with **every** PDF you upload, or only some? If only some, do you notice a pattern?

1. Every PDF, no exceptions
2. Only large PDFs (roughly what size?)
3. Only PDFs from a specific source (e.g., scanned documents, exported from a particular app)
4. It's intermittent — the same file sometimes works and sometimes doesn't
5. Something else

---

### reporter

I don't know, most of them? I haven't exactly been measuring file sizes. I know some of the smaller ones seem to be okay but honestly I'm not keeping track of every single upload. The point is our customers are sending us PDFs and they're coming through broken, and this has been happening for about a week now. Can you just look into it?

---

### triage-agent

That's really helpful — the pattern of smaller files working but larger ones breaking, combined with this starting about a week ago, points us toward a specific kind of regression. One more thing that would help us look in the right place:

Do you upload other file types besides PDFs (images, Word docs, etc.)? If so, are those also coming through broken, or is it just PDFs?

1. We only upload PDFs, so I can't say
2. Other file types are also broken
3. Other file types seem fine — it's just PDFs
4. Not sure

---

### reporter

We mostly deal with PDFs but yeah we do get some images and spreadsheets too. I haven't heard complaints about those but honestly I haven't been checking them as closely. The PDFs are the ones that are obviously broken because you can't open them at all. Can you just look at what changed a week ago? That's when this started.

---

### triage-agent

[RESOLVED] {"title":"File upload corrupts large PDF attachments (size-dependent regression, ~1 week old)","problem":"PDF files attached to tasks are being corrupted during upload, rendering them unopenable. The issue is size-dependent — smaller PDFs appear to work while larger ones are consistently broken. Multiple customers are affected. Other file types (images, spreadsheets) have not been reported as broken but have not been rigorously checked. The issue started approximately one week ago, indicating a regression.","root_cause_hypothesis":"A change deployed ~1 week ago likely introduced a bug in the file upload pipeline that corrupts binary data above a certain size threshold. Common causes for this pattern: (1) a chunked upload change that incorrectly reassembles chunks, (2) a text/encoding transformation (e.g., UTF-8 conversion) being applied to binary streams, (3) a new file size limit or compression step that truncates or re-encodes large files, or (4) a content-type handling change that misidentifies binary files.","reproduction_steps":["Upload a small PDF (<1 MB) to a task and verify it opens correctly","Upload a larger PDF (>5 MB) to a task","Download the uploaded large PDF and attempt to open it","Compare the byte size and checksum of the original vs. downloaded file to confirm data corruption","Repeat with a large non-PDF binary file (e.g., a large image) to determine if the issue is PDF-specific or affects all large binary uploads"],"environment":"Production environment; affects multiple customers. No specific browser/OS constraints reported — likely server-side.","severity":"high","impact":"Multiple customers are unable to use PDF attachments, which is a core part of their workflow. Data loss is occurring (uploaded files are unrecoverable in their corrupted form).","recommended_fix":"Review all commits to the file upload/attachment pipeline from the past 7-10 days. Look specifically for changes to: chunked upload handling, binary stream processing, content-type detection, file size limits, compression, or encoding transformations. Compare the byte stream of an uploaded large PDF against its original to identify where corruption occurs (truncation, encoding mangling, or chunk reassembly errors). A binary diff between the original and corrupted file will quickly reveal the corruption pattern.","proposed_test_case":"Upload PDFs of varying sizes (100KB, 1MB, 5MB, 20MB) and verify that the downloaded files are byte-identical to the originals (matching checksums). Include at least one non-PDF binary file of similar size to confirm the fix isn't format-specific.","information_gaps":["Exact file size threshold where corruption begins (developer can determine via testing)","Whether large non-PDF binary files are also affected (developer can test during investigation)","The specific nature of the corruption — truncation vs. encoding mangling vs. chunk errors (binary diff will reveal)"]}

---
