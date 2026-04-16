# Conversation: file-upload-corruption x omo-prometheus

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted file attachments are definitely something we want to get to the bottom of quickly.

To start narrowing this down: **Was this working correctly before, and if so, when did you first notice the problem?** Also, is this happening with *every* PDF you upload, or only certain ones (e.g., larger files, scanned documents, files from a specific source)?

---

### reporter

It started maybe a week ago? I'm not 100% sure on the exact day but it was recent. And no, it's not every PDF — some of them work fine. I haven't figured out the pattern though. I just know that a bunch of our customers are complaining they can't open the files they uploaded. It's really frustrating because this is a core part of our workflow.

---

### triage-agent

Thanks for the detail — a regression from about a week ago is a really useful data point.

Let's try to find the pattern. Can you check a couple of the corrupted PDFs and tell me:

1. **What are the approximate file sizes** of PDFs that come through corrupted vs. ones that work fine?
2. **If you download one of the corrupted files**, is the downloaded file the same size as the original you uploaded, or is it noticeably smaller/larger?

This will help us figure out whether files are being truncated or mangled during the upload process.

---

### reporter

Look, I don't have time to go check file sizes on every single PDF. I just know some work and some don't. I guess the ones that don't work tend to be bigger files? Like the small quick ones seem okay but I'm not totally sure. As for the download size, I haven't compared them side by side — I just know when I download them they're broken and won't open. Can you just look at whatever changed a week ago? Something obviously broke.

---

### triage-agent

Totally understand — you shouldn't have to play detective on this. One thing that would help us move fast: **could you share (or attach to this issue) one of the corrupted PDFs as downloaded from TaskFlow, along with the original file before upload?** Even just one pair would let us diff them directly and pinpoint exactly what's going wrong (truncation, encoding issue, etc.).

If sharing the original isn't possible, even just the corrupted download would help — we can inspect the file headers to see where the damage is happening.

Also quick question: **are your customers uploading through the web browser, the mobile app, or the API?** Just want to make sure we're looking at the right upload path.

---

### reporter

I don't have the original files handy right now and I'm not going to ask my customers to dig around for them. They're uploading through the web browser. Can someone on your end just look at what changed in the last week? This was working fine before and now it's not.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted after upload — regression affecting larger files via web browser","problem":"PDF files uploaded as task attachments through the web browser are being corrupted. When downloaded, the PDF viewer reports the file as damaged and cannot open it. The issue disproportionately affects larger PDFs while smaller ones tend to work fine. Multiple customers are affected and this is blocking a core workflow.","root_cause_hypothesis":"A change deployed approximately one week ago likely introduced a bug in the file upload or storage pipeline that corrupts larger files. Probable causes include: (1) a chunked upload change that truncates or misassembles chunks beyond a size threshold, (2) an encoding or content-type handling change that mangles binary data for larger payloads, or (3) a middleware/proxy change (e.g., request body size limit, compression) that silently corrupts uploads above a certain size.","reproduction_steps":["Upload a small PDF (under ~1 MB) as a task attachment via the web browser — verify it downloads and opens correctly","Upload a larger PDF (5+ MB) as a task attachment via the web browser","Download the uploaded file","Attempt to open — expect PDF viewer to report corruption","Compare file sizes of original and downloaded file to check for truncation","Inspect file headers (e.g., `hexdump -C`) to identify where corruption begins"],"environment":"Web browser upload path. No specific browser identified. Issue is approximately one week old, indicating a regression tied to a recent deployment.","severity":"high","impact":"Multiple customers are unable to use the file attachment feature for larger PDFs. This is described as a core part of their workflow, so it is actively blocking daily work for affected users.","recommended_fix":"1. Review all commits/deployments from the past 7-10 days that touch the file upload pipeline, storage layer, or any middleware/proxy configuration (e.g., nginx body size limits, multipart handling, streaming/chunking logic). 2. Pull a corrupted file from storage and diff against a known-good upload to identify the corruption pattern (truncation, encoding, chunk boundary issue). 3. Check for any size-based thresholds in the upload path (multipart chunk size, max body size, buffering limits). 4. Verify the Content-Type header is preserved as application/octet-stream or application/pdf through the full upload chain.","proposed_test_case":"Upload PDFs of varying sizes (100 KB, 1 MB, 5 MB, 10 MB, 25 MB) through the web browser, download each, and verify byte-for-byte integrity against the original using checksums (sha256sum). This test should be added as an integration test against the upload endpoint.","information_gaps":["Exact file size threshold where corruption begins — engineering team should determine this via systematic testing","Nature of corruption (truncation vs. encoding vs. chunk reassembly) — requires server-side file inspection","Whether the same file consistently corrupts on re-upload or if the corruption is intermittent","Specific browser(s) affected — reporter did not specify; worth testing across Chrome, Firefox, Safari","Whether the API or mobile upload paths are also affected or only the web browser path"]}

---
