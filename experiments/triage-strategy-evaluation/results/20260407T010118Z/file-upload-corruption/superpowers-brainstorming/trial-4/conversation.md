# Conversation: file-upload-corruption x superpowers-brainstorming

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments are definitely something we want to get to the bottom of quickly.

To help narrow down the cause: is this happening with **every** PDF you upload, or only certain ones?

1. **Every PDF** — no PDF attachment has worked recently
2. **Only large PDFs** — smaller files seem fine
3. **Only some PDFs** — it seems random or you're not sure what distinguishes the broken ones
4. **Something else**

---

### reporter

Honestly I'm not sure, it feels like most of them are broken. Some of the smaller ones might be okay actually, but the ones our customers are sending in are usually bigger documents and those are definitely busted. Can you just tell me when this is going to be fixed? Our team is stuck.

---

### triage-agent

Thanks for that detail — the pattern of larger files breaking while smaller ones seem fine is a really useful clue. That points toward either a file size limit or something going wrong during the upload of bigger files.

Do you have a rough sense of how large the PDFs are that come through broken? No need to be exact — just a ballpark:

1. **Around 5–10 MB**
2. **10–25 MB**
3. **25 MB or larger**
4. **Not sure**, but I can check a couple and get back to you

---

### reporter

I don't know the exact sizes, I don't sit there checking file sizes. They're regular business documents — contracts, reports, that kind of thing. Probably a few MB each? I really don't care about the size, I care that our customers can't access their files. This has been going on for about a week now and it's killing us.

---

### triage-agent

I hear you — I'm not going to ask about file sizes again. Let me share where I think we are so we can get this to the right developer quickly.

**Working theory:** Something changed in how TaskFlow handles file uploads roughly a week ago, and it's corrupting larger PDFs during the upload or storage process.

One question that will help us confirm this and find the fix faster: did PDF attachments work fine before about a week ago, and then suddenly stop? Or has this been a recurring issue that's gotten worse over time?

1. **It was working fine, then suddenly broke** — about a week ago
2. **It's been unreliable for a while**, but got worse recently
3. **We're not sure** — we only noticed it about a week ago

---

### reporter

It was working fine before. Like I said, this started about a week ago. Everything was normal and then suddenly our customers started complaining that they couldn't open their attachments. Something clearly broke on your end.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted on upload — regression introduced ~1 week ago, affects larger files","problem":"PDF files attached to tasks are being corrupted during upload or storage. Customers cannot open the resulting files — PDF viewers report them as damaged. The issue disproportionately affects larger files (typical business documents like contracts and reports), while smaller PDFs may still work. This is a regression: the feature was working correctly until approximately one week ago, when it suddenly broke.","root_cause_hypothesis":"A deployment or configuration change approximately one week ago introduced a bug in the file upload pipeline that corrupts larger files. Likely candidates: a change to chunked upload handling, multipart form parsing, stream processing, or storage write logic that truncates or mangles file data above a certain size threshold. Binary file encoding issues (e.g., inadvertent text-mode processing of binary data) are also possible.","reproduction_steps":["Upload a PDF file of ~5 MB or larger to a task as an attachment","Download the uploaded attachment","Attempt to open the downloaded PDF — it should report as corrupted or damaged","Repeat with a very small PDF (~100 KB) to confirm smaller files are unaffected","Compare the byte content of the original and uploaded files to identify where corruption occurs (truncation, encoding, or byte mangling)"],"environment":"Server-side issue affecting all users. Not browser- or OS-specific. Production environment, occurring since approximately one week ago.","severity":"critical","impact":"Multiple customers are unable to access uploaded PDF attachments, blocking teams that rely on the file attachment feature for daily workflows. The reporter describes their team as 'stuck' and the issue as ongoing for about a week.","recommended_fix":"1. Review all deployments and configuration changes from approximately one week ago, focusing on file upload, attachment processing, and storage subsystems. 2. Compare uploaded file bytes against source files to determine the nature of the corruption (truncation, encoding, byte substitution). 3. Check for changes to: multipart upload handling, stream/buffer processing, file size limits, storage backend configuration, or any middleware that touches file data. 4. If a chunked upload or streaming change is identified, verify that binary data is not being processed as text (e.g., UTF-8 encoding applied to binary streams).","proposed_test_case":"Upload PDFs of varying sizes (100 KB, 1 MB, 5 MB, 10 MB, 25 MB) and verify that each downloaded attachment is byte-identical to the original. Include a regression test that uploads a binary file, downloads it, and asserts a checksum match.","information_gaps":["Exact file size threshold where corruption begins","Whether non-PDF binary files (images, DOCX, ZIP) are also affected or only PDFs","The specific deployment or commit that introduced the regression"]}

---
