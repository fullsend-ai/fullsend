# Conversation: file-upload-corruption x superpowers-brainstorming

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments definitely need to get fixed.

To help narrow down the cause: is this happening with **every** PDF you upload, or only certain ones? For example:

1. **Every PDF** — no PDF upload works at all anymore
2. **Only large PDFs** — small ones seem fine
3. **Only certain PDFs** — some work, some don't, and size doesn't seem to be the pattern
4. **Something else**

Also, did this start recently (e.g., after an update), or is it something you've always experienced?

---

### reporter

Look, I don't have time to go through every PDF we've ever uploaded. All I know is customers are complaining that their PDFs are broken and it's making us look bad. I think the smaller ones might be okay actually, but I'm not sure if that's the pattern or if we just got lucky with those. It definitely started recently — maybe a week ago? We were fine before that.

---

### triage-agent

Totally understand you're pressed for time — I'll keep this quick.

Based on what you've described, my working theory is: **a recent change (roughly a week ago) broke how larger PDF uploads are processed**, likely truncating or corrupting them mid-transfer, while smaller files slip through intact.

To confirm this without a big time investment: could you pick **one** of the broken PDFs and check whether the **file size in TaskFlow** is smaller than the original on your computer? (Right-click → properties on both.) If the uploaded version is noticeably smaller, that points to truncation during upload. If the sizes match, it's more likely an encoding issue.

Does that theory track with what you're seeing?

---

### reporter

Ugh, I don't know, I don't have time to go comparing file sizes on every PDF. But yeah, your theory sounds about right — the smaller ones do seem to work. I just checked one of the broken ones quickly and the file size actually looks about the same as the original, so I don't think it's getting cut off. It's just... broken. The PDF viewer says it's damaged or something. Can you just look at whatever changed a week ago and fix it?

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted on upload — content encoding regression (~1 week ago)","problem":"PDF files uploaded as task attachments are rendered unopenable/damaged by PDF viewers. The issue affects larger PDFs while smaller ones appear to work. File sizes between the original and uploaded copy are approximately the same, ruling out truncation. This is a regression that started roughly one week ago.","root_cause_hypothesis":"A recent change to the file upload pipeline is corrupting binary content during transfer or storage — most likely a character encoding transformation (e.g., binary stream processed as UTF-8 text), an incorrect Content-Transfer-Encoding, or a middleware/filter that re-encodes or transforms the upload body. The file size stays roughly the same because the data isn't being truncated, but byte sequences that are invalid in the assumed encoding are being replaced or mangled. Smaller files are less likely to contain problematic byte sequences, which is why they appear unaffected.","reproduction_steps":["Upload a PDF larger than ~1 MB to a task as an attachment","Download the uploaded attachment","Attempt to open the downloaded file in a PDF viewer","Observe that the viewer reports the file as damaged or corrupted","Compare the file size of the original and downloaded copy — they should be approximately equal, confirming the data is not truncated but corrupted"],"environment":"TaskFlow file attachment feature; affects multiple customers; no specific browser/OS indicated (likely server-side issue given widespread reports)","severity":"high","impact":"Multiple customers are unable to use PDF attachments, a heavily-used feature. Uploaded files are silently corrupted with no error feedback, causing data loss and eroding customer trust.","recommended_fix":"Review all changes to the file upload/attachment pipeline from the past 1–2 weeks. Specifically investigate: (1) any change to how uploaded file streams are read or written — look for text-mode I/O where binary-mode is required, (2) middleware that may process request bodies as text (e.g., body-parser with text/charset handling applied to multipart uploads), (3) changes to storage layer encoding (base64 encode/decode, Content-Type headers), (4) any dependency updates to upload-handling libraries. Compare a hex dump of an original PDF against the stored/downloaded version to identify the exact corruption pattern (e.g., 0xFFFD replacement characters indicating UTF-8 re-encoding of binary data).","proposed_test_case":"Upload PDFs of varying sizes (100 KB, 1 MB, 10 MB) and perform a byte-for-byte comparison (e.g., sha256sum) between the original and the downloaded attachment. All checksums must match exactly. Additionally, add a regression test that uploads a known binary file and asserts the downloaded content is byte-identical.","information_gaps":["Exact file size threshold at which corruption begins (dev can determine via testing)","Whether non-PDF binary attachments (images, zips) are also affected (same root cause likely applies)","The specific commit or deployment that introduced the regression (dev team can correlate with ~1 week ago timeline)"]}

---
