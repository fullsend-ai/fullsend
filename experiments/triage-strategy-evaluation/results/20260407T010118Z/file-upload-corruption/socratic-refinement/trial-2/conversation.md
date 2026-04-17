# Conversation: file-upload-corruption x socratic-refinement

## Issue

**PDF attachments are corrupted after upload**

When customers upload PDF files to their tasks, the files come through broken. You can't open them — the PDF viewer says the file is damaged. This is happening for multiple customers. We use the file attachment feature heavily so this is really impacting our workflow.

---

## Comments

### triage-agent

Thanks for reporting this — corrupted attachments sound really disruptive, especially if your team relies on them heavily.

To help us get to the bottom of this, can you walk me through what the process looks like when this happens? Specifically: what were you (or your customers) doing in TaskFlow when attaching the PDF — for example, creating a new task, editing an existing one, dragging and dropping vs. using the upload button? And once the file shows as 'damaged,' is that when downloading it back from TaskFlow, or does it appear broken immediately after upload?

Also, do you know if this started recently, or has it been an ongoing issue that's just now being reported?

---

### reporter

Look, I don't know all the specifics of how they're uploading — they just use the attach button on tasks, the normal way. The files are fine on their end before uploading. When they download them back from TaskFlow they're corrupted. This started maybe a week ago? It was working fine before that. Can you just look into it? Our team is stuck because we can't share PDFs through TaskFlow anymore.

---

### triage-agent

Totally understand the urgency — I want to help get this unblocked quickly.

One thing that would really help us narrow down the cause: have you noticed whether **every** PDF that gets uploaded comes back corrupted, or are some making it through fine? And do you happen to know if **other file types** (images, Word docs, etc.) are also coming through damaged, or is it just PDFs?

That'll tell us a lot about where to look — whether something broke in how we handle files generally, or something specific to PDFs.

---

### reporter

Now that you mention it, I think some of the smaller PDFs might be going through okay? I'm not 100% sure but one of my reps said her short one-page PDF worked fine. It's the bigger ones that are definitely broken. As for other file types — honestly I mostly care about PDFs, that's what we use. I think someone mentioned an image looking weird but I don't know if that's related. Can someone just look at what changed recently? It was working fine like a week ago and then it wasn't.

---

### triage-agent

[RESOLVED] {"title":"PDF attachments corrupted on download — size-dependent, regression from ~1 week ago","problem":"PDF files attached to tasks via the standard attach button are corrupted when downloaded back from TaskFlow. The corruption appears to be size-dependent: small (e.g., one-page) PDFs may upload correctly, while larger PDFs are consistently broken. The PDF viewer reports the file as damaged. At least one report of an image also looking 'weird' after upload, suggesting the issue may not be PDF-specific.","root_cause_hypothesis":"A recent change (~1 week ago) likely broke the file upload or download pipeline for files above a certain size. Probable causes: (1) a change to chunked upload/download handling that corrupts or truncates larger payloads, (2) an encoding or content-type change (e.g., incorrect Base64 encoding, missing binary mode) that mangles bytes beyond a buffer boundary, or (3) a storage or CDN configuration change that affects larger objects. The size-dependence strongly suggests a boundary condition in streaming, chunking, or buffering logic.","reproduction_steps":["Create or open an existing task in TaskFlow","Attach a multi-page PDF (e.g., 5+ pages, several MB) using the attach button","Download the attached PDF from the task","Attempt to open the downloaded PDF — expect it to be reported as damaged/corrupted","Repeat with a small one-page PDF — expect it to open correctly","Compare uploaded and downloaded file sizes and checksums to identify truncation or byte corruption"],"environment":"Production environment. Affects multiple customers. No specific browser or OS constraints reported — appears to be server-side. Standard file attachment workflow via the task attach button.","severity":"high","impact":"Multiple customers are unable to share PDF documents through TaskFlow, blocking team workflows that depend on PDF attachments. The feature is described as heavily used.","recommended_fix":"1. Check deployment/change logs from approximately one week ago for changes to the file upload pipeline, storage configuration, or related infrastructure. 2. Compare binary content of an uploaded large PDF (server/storage side) against the original to identify where corruption occurs (truncation, encoding, byte substitution). 3. Check for chunked transfer encoding issues, buffer size limits, or content-type handling changes. 4. If a recent deployment is identified, check its diff for changes to multipart upload handling, streaming logic, or storage client libraries.","proposed_test_case":"Upload PDFs of varying sizes (100KB, 1MB, 5MB, 20MB) and verify byte-for-byte integrity by comparing SHA-256 checksums of the original file against the downloaded file. The test should also cover at least one non-PDF file type (e.g., PNG image) to confirm whether the issue is format-specific or general.","information_gaps":["Exact file size threshold where corruption begins (developer can determine via testing)","Whether non-PDF file types are also affected (reporter was unsure; one anecdotal report of a 'weird' image)","Exact date the regression was introduced (reporter estimates ~1 week; deployment logs will be more precise)","Whether corruption is truncation, byte-level mangling, or encoding-related (binary comparison will reveal this)"]}

---
