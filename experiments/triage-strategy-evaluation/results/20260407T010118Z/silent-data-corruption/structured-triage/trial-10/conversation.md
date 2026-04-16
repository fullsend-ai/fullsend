# Conversation: silent-data-corruption x structured-triage

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

To help us investigate, could you share a couple of examples of what the corrupted phone numbers look like? For instance, if a number should be `+1 (555) 867-5309`, what does it actually show as in TaskFlow? Knowing the specific pattern of corruption (truncated, missing country code, digits replaced, etc.) will help us narrow down where the problem is occurring.

---

### reporter

Sure, let me pull up a couple examples. We have a customer in Ireland whose number should be +353 1 234 5678 but in TaskFlow it shows as +35 1 234 5678 — so it looks like the country code got cut short. Another one from Antigua, the number should start with +1268 but it's showing as +12 and then the rest of the number. Meanwhile our US customers with +1 numbers and UK customers with +44 seem fine. So it seems like the longer country codes are getting truncated to just 2 digits maybe? That's the pattern I've noticed at least.

---

### triage-agent

Thanks for those examples — that's a really clear pattern. Country codes longer than two digits are being truncated to two, which explains why US (+1) and UK (+44) numbers are fine but Ireland (+353) and Antigua (+1268) are not.

To help us track down where the truncation is happening: **how were these customer phone numbers entered into TaskFlow?** For example, were they typed in manually through the UI, imported via CSV/spreadsheet, synced from another system through an API, or some mix? And do you know roughly when the bad numbers first appeared — has this always been the case, or did it start after a recent update or import?

---

### reporter

They come in through a nightly batch import — we sync from our Salesforce CRM automatically. Nobody's typing these in manually. As for timing, I think it started maybe two weeks ago? The numbers were definitely correct before that. I'm not sure exactly what changed on your end around that time, but something in the import process seems to have broken. The weird thing is the sync runs every night so it's been overwriting the numbers gradually — we only caught it when support started getting wrong numbers on calls.

---

### triage-agent

That timeline is really helpful — a two-week window gives us something concrete to look at on our side.

Could you let us know what version of TaskFlow you're currently running? You can usually find this under **Settings → About** or in the footer of the admin dashboard. Also, do you know if TaskFlow was updated around the time this started (roughly two weeks ago), or if any changes were made to your Salesforce sync configuration (e.g., field mappings, API version, or a new integration plugin)?

---

### reporter

We're on TaskFlow v2.3.1. I don't think we updated TaskFlow itself around that time, but I'm not 100% sure — I'd have to check with our admin. We definitely didn't change anything on our end with the Salesforce sync configuration though, the field mappings and everything have been the same for months. So whatever changed, it was on the TaskFlow side. Maybe something in how the import processes phone numbers got updated?

---

### triage-agent

[RESOLVED] {"title":"Phone number country codes truncated to 2 digits during Salesforce batch import","problem":"Customer phone numbers with country codes longer than two digits are being truncated to exactly two digits during the nightly Salesforce CRM batch import. Numbers with 1- or 2-digit country codes (e.g., US +1, UK +44) are unaffected. Approximately 50-100 of ~2,000 customer records are corrupted, and the nightly sync continues to overwrite them with truncated values.","root_cause_hypothesis":"A recent change in the phone number parsing or storage logic in the Salesforce import pipeline is truncating the country code field to a maximum of 2 characters. This likely involves a field length constraint, substring operation, or schema change introduced in a TaskFlow update within the last ~2 weeks.","reproduction_steps":["Configure a Salesforce sync with customer records containing international phone numbers with country codes of varying lengths (e.g., +1, +44, +353, +1268)","Run the batch import process","Inspect the imported phone numbers in TaskFlow","Observe that country codes longer than 2 digits are truncated to 2 digits (e.g., +353 becomes +35, +1268 becomes +12)"],"environment":"TaskFlow v2.3.1, Salesforce CRM integration via nightly batch import, no recent changes to Salesforce sync configuration or field mappings on the customer side","severity":"high","impact":"Support team cannot reach customers with international numbers that have 3+ digit country codes. Data corruption is ongoing — each nightly sync overwrites correct numbers with truncated ones, compounding the damage. Approximately 50-100 records currently affected.","recommended_fix":"Investigate the Salesforce import pipeline's phone number parsing logic, specifically any field length constraints or substring operations on the country code portion. Check git history and release notes for changes made ~2 weeks ago to the import module, phone number normalization, or database schema. Look for a column width change, a regex with a hard-coded capture group length, or a VARCHAR constraint on a country_code field.","proposed_test_case":"Import phone numbers with country codes of length 1 (+1), 2 (+44), 3 (+353), and 4 (+1268) via the batch import pipeline and verify all digits are preserved in the stored records.","information_gaps":["Exact TaskFlow release/update history on the customer's instance over the past month","Server-side import logs that might show warnings or data transformation details","Whether the truncation happens at the API ingestion layer, a normalization step, or at database storage"]}

---
