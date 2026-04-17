# Conversation: file-upload-corruption x omc-deep-interview

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments are definitely something we want to get to the bottom of quickly.

To help us narrow down the cause: does this happen with **every** PDF upload, or only some? If only some, have you noticed any pattern — for example, does it seem related to file size, how the PDF was created (scanned vs. digitally generated), or which browser/device is being used to upload?

---

### reporter

Look, I don't know all the specifics. I just know our team uploads PDFs all day and they keep coming back corrupted. I haven't sat there testing every single one. I do know that some of the smaller ones seem to work fine? But honestly I'm not tracking which exact ones break and which don't. It's the important ones — contracts, reports — that seem to be the problem. Can you just look into it? This has been going on for about a week now and our customers are getting frustrated.

---

### triage-agent

Totally understand — we'll dig into this. One thing that would really speed up our investigation: could you (or someone on your team) grab one of the corrupted files directly from TaskFlow and share it with us? Even just the file name and approximate size of one that broke would help. We'd want to compare what was uploaded to what TaskFlow stored, which is often the fastest way to find where the corruption is happening.

---

### reporter

I don't have the original files to compare, that's the whole problem — once they go in they come out broken. I can tell you one that broke recently was a contract PDF, probably around 3-4MB. The file name was something like 'ServiceAgreement_2026.pdf'. Like I said, the smaller stuff seems okay but the big important documents are the ones getting mangled. Can someone on your end just look at what changed recently? This was working fine until like a week ago.

---

### triage-agent

That's really helpful context — the size pattern and the timing are both strong clues. One quick thing that would help us zero in on the root cause: when you download one of these corrupted PDFs, could you check the file size of the downloaded file? For example, if 'ServiceAgreement_2026.pdf' was ~3-4MB when you uploaded it, is the downloaded version roughly the same size, noticeably smaller, or does it look about right? Also — are you downloading these through the browser, or through our API/integrations? This will help us figure out whether the file is getting corrupted on the way *in* or on the way *out*.

---

### reporter

I don't know, I didn't compare the file sizes byte by byte. It looks about the same I guess? I just know when I try to open it, it's broken. We download through the browser like normal people. Look, you said the timing is a strong clue — it started about a week ago. Something changed on your end, right? Can you just check your recent updates instead of asking me to do QA for you?

---

### triage-agent

Totally hear you, and we are pulling up recent changes on our end — the one-week timeline is exactly the kind of lead we need.

One last thing that would help us look in the right place: does your team upload other types of files too — like Word docs, images, or spreadsheets? If so, are those also coming back broken, or is it just PDFs?

---

### reporter

Yeah we upload other stuff too. Now that you mention it, I think one of the team mentioned an image looking weird the other day but I didn't think much of it. Mostly it's the PDFs that are obviously broken though. Can you please just check whatever you changed a week ago? I feel like we keep going back and forth here and nothing's getting fixed.

---

### triage-agent

[RESOLVED] {"title":"File uploads corrupted for larger files (~3MB+) since recent deployment — PDFs and possibly images affected","problem":"Files uploaded to task attachments are corrupted when downloaded. PDF viewers report files as damaged. The issue primarily affects larger files (3-4MB range), while smaller files appear to work correctly. At least one image was also reported as looking 'weird' after download, suggesting the corruption is not PDF-specific. The downloaded file size appears roughly preserved, pointing to content corruption rather than truncation.","root_cause_hypothesis":"A deployment or infrastructure change approximately one week ago introduced a corruption in the file upload or download pipeline that affects files above a certain size threshold. Since file sizes appear preserved but content is corrupted, likely causes include: (1) an encoding or content-type transformation applied incorrectly to binary data (e.g., UTF-8 re-encoding of binary streams), (2) a change in multipart upload handling or chunked transfer that corrupts data at chunk boundaries, or (3) a middleware or proxy change (e.g., response compression, content transformation) that mangles binary content above a buffering threshold.","reproduction_steps":["Upload a PDF file in the 3-4MB range to a task attachment via the browser UI","Download the same file from the task via the browser UI","Attempt to open the downloaded PDF — it should appear corrupted/damaged","Repeat with a small PDF (<1MB) — this should work correctly","Repeat with a large non-PDF binary file (e.g., a 3-4MB image) to confirm cross-format corruption"],"environment":"Browser-based uploads and downloads. No specific browser identified. Multiple customers affected across the team. Production environment.","severity":"high","impact":"Multiple customers are unable to use file attachments for business-critical documents (contracts, reports). The feature is heavily used and has been broken for approximately one week. Workaround is limited to using only small files.","recommended_fix":"1. Review all deployments and infrastructure changes from approximately one week ago (late March / early April 2026). 2. Compare the binary content of a stored file against its original to identify where corruption occurs (upload path vs. storage vs. download path). 3. Check for changes in: upload middleware, content-type handling, response streaming/compression, proxy or CDN configuration, any size-based thresholds (chunked uploads, multipart boundaries). 4. Specifically look for any encoding transformation being applied to binary streams (e.g., text encoding on binary data). 5. Test the specific file 'ServiceAgreement_2026.pdf' if still available in storage.","proposed_test_case":"Upload binary files of varying sizes (500KB, 1MB, 2MB, 4MB, 10MB) across multiple formats (PDF, PNG, DOCX). Download each and perform a byte-for-byte comparison (SHA-256 hash) between the original and downloaded file. The test passes when all hashes match. Additionally, add a regression test that uploads a known binary file through the full upload/download pipeline and asserts byte-level integrity.","information_gaps":["Exact size threshold where corruption begins (reporter only noted 'smaller ones seem fine')","Specific browser(s) and OS in use (though likely not browser-specific given multiple customers affected)","Whether the corruption occurs on upload (stored file is already corrupt) or on download (stored file is intact but served incorrectly) — needs server-side investigation","Exact deployment or change from ~1 week ago that correlates with the onset"]}

---
