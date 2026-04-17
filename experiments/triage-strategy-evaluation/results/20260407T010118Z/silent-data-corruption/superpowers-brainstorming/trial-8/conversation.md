# Conversation: silent-data-corruption x superpowers-brainstorming

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely worth investigating quickly.

How did these customer phone numbers originally get into TaskFlow?

1. **Manual entry** — your team typed them in through the TaskFlow UI
2. **Bulk import** — you imported them from a CSV/spreadsheet
3. **Integration/sync** — they come from another system (e.g., a CRM, billing platform)
4. **Migration** — they were migrated from an older system when you started using TaskFlow
5. **Something else**

This will help us narrow down where the corruption is happening.

---

### reporter

They come from our CRM — we use Salesforce. There's a nightly sync job that imports customer data into TaskFlow automatically. It's been running for a while without issues, but I think the phone number problems started maybe a couple weeks ago? I'm not 100% sure on the exact timing but our support team started noticing bad numbers recently.

---

### triage-agent

That's really helpful, thank you. The key thing to figure out next is *where* the corruption is happening. Have you checked whether the affected phone numbers are still correct in Salesforce?

1. **Yes, they're correct in Salesforce** — the numbers look fine there but are wrong in TaskFlow
2. **No, they're wrong in both** — Salesforce also has the bad numbers
3. **I haven't checked yet** — I can take a look

This will tell us whether the problem is in the sync job itself or upstream.

---

### reporter

I checked a few of the affected numbers in Salesforce and they look correct there. So the problem seems to be happening during the sync into TaskFlow. The numbers in Salesforce have the full phone number with country code and everything, but in TaskFlow they're cut short.

---

### triage-agent

Good news — that narrows it down to the sync job. One more thing that will really help pinpoint the cause: can you share an example or two of what a corrupted number looks like?

For instance, if a Salesforce number is `+1-555-867-5309`, what does it look like in TaskFlow? Specifically:

1. **Truncated to a fixed length** — e.g., `+1-555-867` (cut off after N characters)
2. **Country code stripped** — e.g., `555-867-5309` (leading `+1` removed)
3. **Non-digit characters removed and then truncated** — e.g., `15558675` (digits only, cut short)
4. **Something else entirely**

The exact shape of the corruption will point us straight at the bug.

---

### reporter

So I pulled up a few examples. One customer in Ireland has +353 1 234 5678 in Salesforce but in TaskFlow it shows as +35 1 234 5678 — like the country code got chopped. Another one from the Caribbean, I think Antigua, has +1268 in Salesforce but it's showing as +12 in TaskFlow. But the weird thing is, our US and UK customers seem fine — their numbers with +1 and +44 look correct. So it's not all numbers, it seems like the longer country codes are getting cut short? I'm not sure if that helps narrow it down.

---

### triage-agent

[RESOLVED] {"title":"Salesforce sync truncates phone country codes longer than 2 digits","problem":"The nightly Salesforce-to-TaskFlow sync job is corrupting international phone numbers by truncating country codes to 2 digits. Numbers with 3+ digit country codes (e.g., Ireland +353, Antigua +1268) are stored with only the first 2 digits of the country code, while 1-2 digit codes (US +1, UK +44) are unaffected. Approximately 50-100 of ~2,000 customer records are impacted.","root_cause_hypothesis":"The sync job or TaskFlow's phone number storage has a 2-character limit on the country code portion. This is most likely either: (a) a database column storing the country code is defined as VARCHAR(2) or CHAR(2), or (b) the sync job's parsing logic uses a regex or substring that assumes country codes are at most 2 digits. A recent change to the sync job, the phone field schema, or the parsing library likely introduced this constraint — the sync had been working previously.","reproduction_steps":["Ensure a Salesforce contact has a phone number with a 3+ digit country code (e.g., +353 1 234 5678 for Ireland)","Run the nightly sync job (or trigger it manually)","Check the resulting phone number in TaskFlow — the country code will be truncated to 2 digits (e.g., +35 1 234 5678)","Repeat with a +1 or +44 number to confirm those sync correctly"],"environment":"Salesforce-to-TaskFlow nightly sync job. Problem started approximately 2 weeks ago. Affects international numbers with 3+ digit country codes (ITU-T E.164 codes like +353, +1268, +880, etc.).","severity":"high","impact":"Support team cannot reach affected customers by phone. Estimated 50-100 records corrupted out of ~2,000. All customers with 3+ digit country codes are affected, meaning international customers outside NANP (+1) and a few major countries are unreachable. Data integrity issue that worsens with each nightly sync if new records are added.","recommended_fix":"1. Inspect the sync job's phone number parsing — look for a field, column, or regex that limits the country code to 2 characters. Check for recent changes (~2 weeks ago) to the sync job, schema migrations, or phone-field validation. 2. Fix the constraint to support country codes up to 4 digits (the ITU-T maximum). 3. Re-sync affected records from Salesforce to restore correct phone numbers. 4. Add a validation check or integration test that syncs a phone number with a 3+ digit country code.","proposed_test_case":"Sync a contact from Salesforce with phone number +353 1 234 5678 (Ireland) and assert that TaskFlow stores exactly +353 1 234 5678. Include additional cases for +1268 (4-digit), +44 (2-digit), and +1 (1-digit) to cover all country code lengths.","information_gaps":["Exact date the problem started (reporter estimated ~2 weeks ago)","Whether any changes were deployed to the sync job or phone field schema around that time","Whether already-corrupted records are overwritten on each sync or only new/modified records are affected"]}

---
