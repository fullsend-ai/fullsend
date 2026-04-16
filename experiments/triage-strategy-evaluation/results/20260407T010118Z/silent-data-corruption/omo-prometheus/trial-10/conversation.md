# Conversation: silent-data-corruption x omo-prometheus

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are serious and we want to get to the bottom of it.

To start narrowing this down, can you share 2-3 concrete examples of what the corrupted phone numbers look like compared to what they should be? For instance, if a number should be `+1 (555) 867-5309`, what is actually stored in TaskFlow? Knowing the exact pattern of corruption (truncated from the left? right? missing country code? digits replaced?) will help us identify the root cause quickly.

---

### reporter

Sure, let me pull up a couple examples. One customer in Ireland had their number as +353 1 234 5678 in our CRM, but in TaskFlow it shows as +35 1 234 5678 — so it's like the country code got chopped. Another one, a customer in Antigua, their number starts with +1268 but in TaskFlow it just shows +12 and then the rest of the number. Meanwhile our US and UK customers seem fine, their numbers look correct. It's weird because it seems like only certain international numbers are affected.

---

### triage-agent

Thanks for those examples — that's very helpful. The pattern is clear: country codes longer than 2 digits are getting truncated (Ireland's `+353` → `+35`, Antigua's `+1268` → `+12`), while shorter codes like US (`+1`) and UK (`+44`) are unaffected.

This narrows things down significantly. My key question: **when did these numbers go wrong?**

- Were these customer records imported/migrated into TaskFlow from your CRM at some point? If so, when?
- Or were they entered directly into TaskFlow (manually or via API)?
- Have you noticed whether numbers that were previously correct in TaskFlow have changed, or have they been wrong since the records were created?

This will tell us whether we're looking at an import/migration bug versus an ongoing data entry or storage issue.

---

### reporter

We have a nightly import job that syncs customer data from our Salesforce CRM into TaskFlow. So the records weren't entered manually — they come in automatically every night. I'm pretty sure the numbers were correct before because our support team has been calling these customers for months without issues. I think this started maybe two weeks ago? I remember because that's around when someone on our dev team mentioned they'd made some update to how phone numbers are handled — something about supporting international formats better, ironically. I'm not 100% sure on the exact date but it was definitely recent, like within the last couple weeks. The numbers in our CRM are still correct, it's only the TaskFlow side that's messed up.

---

### triage-agent

That timeline with the international format change lines up perfectly with the pattern we're seeing — strong lead.

One critical question before I can hand this off to a developer: **is the data actually wrong in TaskFlow's database, or could it just be displaying incorrectly?** Could you or someone on your dev team check the raw phone number value stored in TaskFlow's database for one of those affected records (e.g., the Ireland customer)? If the DB has `+35 1 234 5678`, we're dealing with data corruption at import time and will need to re-sync affected records from Salesforce. If the DB has the full `+353 1 234 5678`, it's a display/formatting bug — much easier to fix with no data loss.

Also, if your dev team can point me to the specific PR or commit from ~2 weeks ago that changed phone number handling, that would let a developer go straight to the root cause.

---

### reporter

Honestly I'm not really sure how to check the raw database value — I'm not a developer myself, I mainly just use the TaskFlow interface. But when I export the records from TaskFlow, the numbers come out truncated too, so I'm pretty sure it's not just a display thing. The data seems actually wrong in the system.

As for the specific commit or PR, I'd have to ask the developer who made that change. I think it was someone on our team but I don't remember exactly who. I can try to track that down but it might take me a bit. Is there anything else I can check from my end in the meantime?

---

### triage-agent

[RESOLVED] {"title":"Phone number country codes truncated to 2 digits during Salesforce nightly import since ~2 weeks ago","problem":"Customer phone numbers with country codes longer than 2 digits are being truncated during the nightly Salesforce-to-TaskFlow import. For example, Ireland's +353 becomes +35, Antigua's +1268 becomes +12. US (+1) and UK (+44) numbers are unaffected because their country codes are already ≤2 digits. Approximately 50-100 of ~2,000 customer records are affected, and the corrupted data persists in storage (confirmed via export).","root_cause_hypothesis":"A code change made approximately 2 weeks ago to improve international phone number format handling likely introduced a hard truncation of the country code field to 2 digits (or parses/slices the country code incorrectly). The import pipeline applies this faulty parsing when writing Salesforce data into TaskFlow's database, corrupting numbers with 3+ digit country codes at write time.","reproduction_steps":["Identify a customer record in Salesforce with a country code longer than 2 digits (e.g., Ireland +353, India +91 should be fine but +91x would not be)","Wait for or manually trigger the nightly Salesforce-to-TaskFlow import sync","Compare the phone number in TaskFlow to the source value in Salesforce","Observe that the country code is truncated to 2 digits in TaskFlow"],"environment":"TaskFlow production environment with nightly Salesforce CRM import job. Issue began approximately 2 weeks ago following a code change to international phone number handling.","severity":"high","impact":"Support team cannot reach ~50-100 international customers by phone. Data integrity is compromised for all records with country codes longer than 2 digits. The corruption is ongoing — each nightly import re-corrupts any manually corrected records. Domestic (US) and UK customers are unaffected.","recommended_fix":"1. Search git history from the past 2-3 weeks for changes to phone number parsing/formatting/import logic. 2. Review how country codes are extracted or validated — look for a hard-coded length limit of 2, a substring/slice operation like `code[:2]`, or a regex that captures only 2 digits for the country code. 3. Fix the parsing to support variable-length country codes (1-3 digits per E.164). 4. After deploying the fix, run a one-time re-sync from Salesforce to repair the ~50-100 corrupted records. Verify by spot-checking Ireland (+353) and other 3-digit country code records.","proposed_test_case":"Unit test: pass phone numbers with 1-digit (+1), 2-digit (+44), and 3-digit (+353) country codes through the import parsing function and assert the full number is preserved. Integration test: simulate a Salesforce import payload containing an Irish number (+353 1 234 5678) and verify the stored value in TaskFlow's database matches exactly.","information_gaps":["Exact commit or PR that changed phone number handling ~2 weeks ago (dev team can find via git log)","Whether the corruption affects only the import path or also manual entry and API writes","Complete list of affected country codes beyond Ireland (+353) and Antigua (+1268) — a DB query filtering for records with country codes ≤2 digits where the source country should have 3 digits would enumerate these"]}

---
