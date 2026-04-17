# Conversation: silent-data-corruption x omo-prometheus

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are serious and we want to get to the bottom of it.

To start narrowing this down: **when did your team first notice the incorrect numbers, and can you tell whether this is a recent change?** Specifically:

1. Were these phone numbers correct at some point and then became corrupted, or is it possible they were never stored correctly?
2. How are customer phone numbers entered into TaskFlow — manual entry, CSV import, API integration, or some combination?

This will help us figure out whether we're looking at a data corruption issue vs. a problem at the point of entry.

---

### reporter

The numbers were definitely correct before — we've been using TaskFlow for over a year and this only started recently. I think it's been going on for maybe two weeks or so? Our support team just caught it a few days ago when calls started failing.

We use a nightly batch import that syncs from our Salesforce CRM. That's how all the customer data gets into TaskFlow. So the numbers are correct in Salesforce but wrong in TaskFlow, which is how we confirmed it's not a typo situation.

---

### triage-agent

Thanks for confirming the source — that's very helpful. The nightly Salesforce import narrows this down significantly.

Can you give me **2-3 specific examples** of what the corrupted numbers look like compared to what's in Salesforce? For instance, if the correct number is `+1-555-867-5309`, what does TaskFlow show instead? I'm looking for the exact pattern — are digits being dropped from the beginning, the end, or is the format changing in some other way?

This will help us pinpoint whether it's a truncation issue (field length), a formatting/parsing change, or something else entirely.

---

### reporter

Let me check... okay so for example, we have a customer in Ireland whose number in Salesforce is +353 1 234 5678, but in TaskFlow it shows up as +35 1 234 5678 — so the country code got cut short. Another one is a customer in Antigua, their number is +1268 555 1234 in Salesforce but TaskFlow has +12 555 1234. I think there's also one from India (+91 something) that looks fine though. It seems like it's the beginning of the number that's getting messed up, specifically the country code part? US and UK numbers seem okay from what I've checked.

---

### triage-agent

Thanks for those examples — they're very revealing. I think I see the pattern:

- **+353** (Ireland, 3-digit country code) → truncated to **+35**
- **+1268** (Antigua, 4-digit country code) → truncated to **+12**
- **+91** (India, 2-digit country code) → **fine**
- **+1** (US) and **+44** (UK) → **fine**

It looks like the country code portion of the phone number is being **truncated to exactly 2 digits**. Numbers with 1- or 2-digit country codes are unaffected, but anything longer gets clipped.

Two things would let me close this out:

1. **Does that pattern match what you're seeing across your affected records?** If you spot any exceptions (e.g., a 3+ digit country code that's fine, or a 2-digit code that's broken), that would be important to know.
2. **Did anything change in your TaskFlow setup roughly 2 weeks ago?** For example: a TaskFlow version update, a change to the import job configuration, or a new field mapping in the Salesforce sync? Even a minor update could have introduced a parsing change that enforces a 2-character limit on the country code field.

---

### reporter

Yeah, that pattern sounds exactly right from what I can tell. I spot-checked a few more and the ones with longer country codes are all cut to 2 digits. I haven't found any exceptions so far.

As for changes — I'm not sure on our end, we didn't change the import config or anything like that. But I think there was a TaskFlow update around that time? I vaguely remember seeing something about version 2.3.1 being deployed, but I'm not 100% sure if the timing lines up exactly. We didn't change any field mappings on the Salesforce side though, I can confirm that.

---

### triage-agent

[RESOLVED] {"title":"Nightly Salesforce import truncates phone number country codes to 2 digits","problem":"Customer phone numbers imported from Salesforce via the nightly batch sync are having their country codes truncated to exactly 2 digits. Numbers with 1- or 2-digit country codes (US +1, UK +44, India +91) are unaffected, but 3+ digit country codes are clipped (Ireland +353 → +35, Antigua +1268 → +12). Approximately 50-100 of ~2,000 customer records are affected. The corruption is in the stored data — support staff get wrong numbers when calling affected customers.","root_cause_hypothesis":"A recent change (likely in TaskFlow v2.3.1, deployed ~2 weeks ago) introduced a 2-character limit on the country code portion of phone numbers during the Salesforce import parsing/storage path. This could be a schema migration that shortened a column, a new validation rule, a regex change in the phone number parser, or a field-length constraint added to the country code component of a structured phone number type.","reproduction_steps":["Set up a Salesforce sync with customer records containing international phone numbers with 3+ digit country codes (e.g., Ireland +353, Antigua +1268)","Run the nightly batch import (or trigger it manually)","Compare the phone numbers stored in TaskFlow against the Salesforce source","Observe that country codes are truncated to 2 digits while the remaining digits of the number are preserved"],"environment":"TaskFlow (believed to be v2.3.1), nightly batch import from Salesforce CRM. No changes were made to the import configuration or Salesforce field mappings. Issue began approximately 2 weeks ago.","severity":"high","impact":"~50-100 customer records have corrupted phone numbers. Support team cannot reach affected international customers. All customers with country codes of 3+ digits are affected on every import cycle, so the problem is ongoing and may be re-corrupting any manual fixes.","recommended_fix":"1. Check the v2.3.1 changelog and diff for any changes to phone number parsing, storage schema, or import field mappings. 2. Look for a country code field/column with a VARCHAR(2) or equivalent constraint, or a parsing regex that captures only 2 digits for the country code. 3. Fix the constraint to allow up to 4 digits for country codes (the ITU maximum is 3, but +1xxx Caribbean codes are effectively 4). 4. Re-import affected records from Salesforce to restore correct data. 5. Consider adding a validation check to the import pipeline that flags phone numbers that changed length between source and destination.","proposed_test_case":"Import a batch of phone numbers covering all country code lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4-digit Caribbean). Verify that all numbers are stored exactly as provided, with no truncation. Regression test should assert character-level equality between source and stored values for the country code portion.","information_gaps":["Exact TaskFlow version — reporter believes v2.3.1 but is not certain of timing","Whether already-corrupted records get re-corrupted on each import cycle (likely yes, since Salesforce source is correct and import runs nightly)","Whether the truncation happens at parse time, storage time, or display time (calling failures confirm it is at least stored incorrectly)"]}

---
