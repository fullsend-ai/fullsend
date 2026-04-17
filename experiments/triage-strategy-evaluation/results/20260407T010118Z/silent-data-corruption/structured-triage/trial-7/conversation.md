# Conversation: silent-data-corruption x structured-triage

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

Could you share a couple of examples of what the incorrect phone numbers look like compared to what they should be? For instance, if a number should be `+1 (555) 867-5309`, what does the mangled version look like in TaskFlow? (Feel free to redact digits for privacy — the format/length difference is what matters most.)

This will help us narrow down whether it's a truncation, encoding, or formatting issue.

---

### reporter

Yeah, so for example we have a customer in Ireland whose number should be +353 1 234 5678 but in TaskFlow it shows up as +35 1 234 5678 — the country code got cut short. Similarly we have a customer from Antigua, their number starts with +1268 but it's showing as +12 followed by the rest of the number. But our US and UK customers seem fine — like +1 and +44 numbers look correct. So it seems like it's specifically the longer country codes that are getting chopped.

---

### triage-agent

Thanks for those examples — that's a really clear pattern. Country codes longer than 2 digits are being truncated to 2, which explains why +1 (US) and +44 (UK) look fine but +353 (Ireland) and +1268 (Antigua) don't.

To help us track down where the truncation is happening: how are these phone numbers getting into TaskFlow? For example, are they entered manually through the UI, imported via CSV/spreadsheet, synced from another system through an API, or some mix? And do you know if numbers that were previously correct have changed, or if they've looked this way since they were first added?

---

### reporter

They come in through a nightly batch import — we sync from our Salesforce CRM automatically. And yes, these numbers were definitely correct before. I'm pretty sure this started maybe two weeks ago? Our support team only caught it recently but when I spot-checked some records I could see the numbers had been fine earlier. I think something changed in how the import processes phone numbers, because the data in Salesforce is still correct.

---

### triage-agent

That timeline is really helpful — knowing it started roughly two weeks ago and that the Salesforce data is still correct strongly points to something changing on the TaskFlow/import side.

Do you know if anything changed around that time? For example:
- Was TaskFlow updated to a new version?
- Was the import script or sync configuration modified?
- Were there any database migrations or schema changes?

Even a rough version number or deployment date would help the team pinpoint exactly which change introduced the truncation.

---

### reporter

I'm not sure about the exact details, but I think one of the developers mentioned they updated something related to phone number formatting or validation around that time — something about adding support for international numbers. I don't know the specifics of what was changed though. We're on TaskFlow v2.3.1 if that helps.

---

### triage-agent

[RESOLVED] {"title":"Phone number country codes truncated to 2 digits during Salesforce batch import","problem":"The nightly batch import from Salesforce is truncating international phone number country codes to 2 digits. Numbers with 1- or 2-digit country codes (+1, +44) are unaffected, but longer codes like +353 (Ireland) become +35 and +1268 (Antigua) becomes +12. Approximately 50-100 of ~2,000 customer records are affected.","root_cause_hypothesis":"A recent change to phone number formatting or validation (likely in the v2.3.1 timeframe) introduced a hard limit of 2 characters for the country code field during import parsing. The parsing logic likely splits the phone number assuming all country codes are 1-2 digits, truncating 3- and 4-digit codes.","reproduction_steps":["Ensure a Salesforce record exists with a phone number using a 3+ digit country code (e.g., +353 1 234 5678 for Ireland)","Run the nightly batch import sync from Salesforce","Check the imported record in TaskFlow — the country code will be truncated to 2 digits (+35 1 234 5678)"],"environment":"TaskFlow v2.3.1, nightly batch import from Salesforce CRM","severity":"high","impact":"50-100 customer contact records have corrupted phone numbers. Support team cannot reach affected customers. Data corruption is ongoing with each nightly import, potentially re-corrupting any manual fixes.","recommended_fix":"Review the phone number formatting/validation change introduced ~2 weeks ago (likely in v2.3.1 or a recent deployment). Look for a hard-coded country code length limit or incorrect substring/slice operation in the import pipeline's phone number parser. Country codes range from 1 to 4 digits per the E.164 standard. After fixing, run a one-time repair job to re-import affected numbers from Salesforce.","proposed_test_case":"Unit test the phone number import parser with country codes of varying lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4 digits). Assert that the full country code and subscriber number are preserved in each case.","information_gaps":["Exact commit or changeset that introduced the formatting/validation change","Whether any error logs are generated during import for the affected numbers","Whether manually entered phone numbers with long country codes are also affected (UI path vs import path)"]}

---
