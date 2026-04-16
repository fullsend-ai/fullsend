# Conversation: silent-data-corruption x structured-triage

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

To help us investigate: can you tell us how these customer records were created or last updated? For example, were the affected phone numbers entered manually through the UI, imported via CSV/bulk upload, migrated from another system, or possibly modified by an automated process? Any pattern you've noticed in *when* the bad data appeared would be very helpful.

---

### reporter

Our customer records come in through a nightly batch import from our Salesforce CRM. It syncs automatically every night. I'm not sure exactly when the bad numbers started appearing, but our support team started noticing maybe a week or so ago? It could have been going on a bit longer than that though. The numbers definitely weren't entered manually — they all come through that import process. The weird thing is not all numbers are affected, just some of them. The ones I've spot-checked that are wrong seem to be international numbers, but I haven't done a thorough audit or anything.

---

### triage-agent

Thanks for the detail about the Salesforce nightly import — that's really helpful, and the international-number pattern is a great clue.

Could you grab one or two examples of an affected record and share what the phone number looks like in TaskFlow versus what it should be (i.e., what it looks like in Salesforce)? For instance, is the number being shortened, is the leading `+` or country code getting dropped, or is it garbled in some other way?

A concrete before/after comparison will help us pinpoint exactly where the formatting is going wrong in the import pipeline.

---

### reporter

Sure, let me pull up a couple examples. We have a customer in Ireland whose number in Salesforce is +353 1 234 5678, but in TaskFlow it shows as +35 1 234 5678 — so it looks like the country code got cut short. Another one is a customer in Antigua, their number should be +1268 555 1234 but TaskFlow has it as +12 555 1234. So yeah, it looks like the country code is getting truncated somehow. Our US and UK customers seem fine though — I checked a few of those and they match what's in Salesforce.

---

### triage-agent

[RESOLVED] {"title":"Salesforce import truncates international phone country codes to 2 digits","problem":"The nightly Salesforce CRM batch import is corrupting international phone numbers by truncating their country codes to 2 digits. An estimated 50-100 out of ~2,000 customer records are affected, causing support staff to reach wrong numbers.","root_cause_hypothesis":"The phone number parsing or storage logic in the Salesforce import pipeline is applying a fixed 2-digit limit to the country code portion of international numbers. Numbers with 1- or 2-digit country codes (US +1, UK +44) pass through correctly, while numbers with 3- or 4-digit country codes (Ireland +353, Antigua +1268) are truncated to 2 digits.","reproduction_steps":["Set up or locate the nightly Salesforce CRM import integration","Ensure the Salesforce source contains a contact with an international phone number whose country code is longer than 2 digits (e.g., +353 1 234 5678 for Ireland)","Run the import (or wait for the nightly sync)","Check the imported record in TaskFlow — the country code will be truncated to 2 digits (e.g., +35 1 234 5678)"],"environment":"TaskFlow with nightly batch import from Salesforce CRM (automated sync). Issue observed for at least ~1 week. Affects international numbers with 3+ digit country codes.","severity":"high","impact":"50-100 customer contact records have corrupted phone numbers, preventing support staff from reaching those customers. Data corruption is ongoing with each nightly import, so affected records are being re-corrupted even if manually corrected. Primarily impacts customers in countries with 3- or 4-digit calling codes.","recommended_fix":"Investigate the phone number parsing logic in the Salesforce import pipeline. Look for a hard-coded 2-character substring or regex group capturing the country code. The fix should preserve the full country code as provided by Salesforce (likely stored in E.164 format). After fixing the parser, run a one-time re-import or correction pass to repair the ~50-100 affected records.","proposed_test_case":"Import a batch of phone numbers including: +1 (US, 1-digit code), +44 (UK, 2-digit), +353 (Ireland, 3-digit), +1268 (Antigua, 4-digit). Verify all are stored exactly as provided in the source data, with no truncation of the country code.","information_gaps":["Exact TaskFlow version and import pipeline configuration","Precise date the truncation started (could correlate with a deployment or config change)","Whether import logs show any warnings or errors during phone number processing","Whether the issue also affects phone number fields other than the primary contact number"]}

---
