# Conversation: silent-data-corruption x omc-deep-interview

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

Could you share a couple of examples of what the incorrect phone numbers look like compared to what they should be? For instance, are digits missing from the beginning, the end, or somewhere in the middle? And do you know if these are numbers that were entered recently, or have they been in the system for a while and only just noticed?

---

### reporter

Yeah, so looking at a few examples — it seems like the country code part is getting cut short. Like we have customers in Ireland whose numbers should start with +353 but they show as +35 followed by the rest of the number. And we have some customers in the Caribbean, I think Antigua, where the code should be +1268 but it's showing as +12. Weirdly, our US and UK customers seem fine — their numbers with +1 and +44 look correct.

These numbers have been in the system for a while, but I think the issue is somewhat recent. I'd say maybe the last two weeks or so? Our team syncs customer data from our CRM every night, so it's possible the bad numbers came in through that process. We didn't notice right away because it was gradual — just a few wrong numbers here and there until our support team started hitting enough bad numbers to flag it.

---

### triage-agent

That truncation pattern is really helpful — it looks like country codes longer than two digits are getting clipped to exactly two characters, which is a strong clue.

You mentioned the nightly CRM sync as a possible source. Can you tell me a bit more about how that sync works? Specifically: did anything change about two weeks ago — a migration, a CRM update, a change to the sync script or its configuration, or maybe a schema change on either side? And do you know if the phone numbers look correct in the CRM itself, or are they already truncated there?

---

### reporter

The numbers are definitely correct in the CRM — we checked Salesforce and all the phone numbers look right there, so the issue is happening on the TaskFlow side during or after the import. As for what changed two weeks ago, I'm not totally sure on the details but I think one of our developers mentioned updating something related to how phone numbers are formatted or normalized during the import. I don't know the specifics of the sync script myself, but the timing lines up. The sync runs every night and pulls records from Salesforce into TaskFlow — it's been running for months without issues until recently.

---

### triage-agent

Thanks — knowing the data is correct in Salesforce really narrows this down.

One more thing that would help us figure out the right fix: when you look at the affected phone numbers in TaskFlow, do you know if the numbers are actually stored incorrectly (e.g., if you export the data or look at a record's edit history, the digits are genuinely missing), or is it possible the full number is still there but just being displayed in a truncated way on screen?

This matters because if the data itself has been overwritten by the nightly sync, we'll need to restore those records from Salesforce in addition to fixing the sync — versus if it's just a formatting issue on display, the underlying data may still be intact.

---

### reporter

Yeah, the data is actually wrong in the database — it's not just a display thing. I checked by looking at a few records and the digits are genuinely missing. We don't really have edit history in TaskFlow for these fields unfortunately, so we can't see what they were before. The nightly sync overwrites the phone numbers each time it runs, so the bad values have been getting written in over the last couple weeks. We'll need to restore from our CRM backups to fix the affected records, but obviously we need the sync issue fixed first or it'll just break them again.

---

### triage-agent

[RESOLVED] {"title":"Nightly CRM sync truncates country codes longer than 2 digits in phone numbers","problem":"The nightly Salesforce-to-TaskFlow sync is writing truncated phone numbers into the database. Country codes longer than two characters are being clipped to exactly two characters (e.g., +353 becomes +35, +1268 becomes +12). Numbers with 1- or 2-digit country codes (+1, +44) are unaffected. Approximately 50–100 of ~2,000 customer records are corrupted. The data is genuinely wrong in the database, not a display issue.","root_cause_hypothesis":"A change made approximately two weeks ago to the phone number formatting or normalization logic in the CRM sync script is truncating the country code portion of phone numbers to a maximum of two characters. This is likely a hard-coded length limit, an incorrect substring/slice operation, or a regex pattern that assumes all country codes are 1–2 digits.","reproduction_steps":["Identify the recent change to the sync script's phone number formatting/normalization (check git history for commits ~2 weeks ago)","Run the sync logic against a test record with a phone number that has a 3+ digit country code (e.g., +353 1 234 5678 for Ireland)","Observe that the resulting phone number is truncated to +35 1 234 5678","Repeat with a 1-digit country code (e.g., +1 555 123 4567) and confirm it is unaffected"],"environment":"TaskFlow production database, nightly sync process pulling from Salesforce CRM. Issue introduced approximately two weeks ago with a code change to the sync script's phone number handling.","severity":"high","impact":"Support team cannot reach affected customers by phone. ~50–100 records currently corrupted and the number grows with each nightly sync as records are re-processed. Data loss is real (overwritten, not reversible without re-import from Salesforce).","recommended_fix":"1. Review git history for the sync script's phone number normalization code changed ~2 weeks ago. 2. Fix the truncation logic to support country codes of 1–4 digits. 3. After deploying the fix, re-run the sync (or a targeted restore) to pull correct phone numbers from Salesforce for all affected records.","proposed_test_case":"Unit test the phone number normalization function with country codes of varying lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4 digits). Assert that the full country code and subscriber number are preserved in each case.","information_gaps":["Exact file and commit that introduced the regression (discoverable via git history)","Whether the truncation happens in application code or at the database schema level (e.g., a column width change)","Full scope of affected country codes beyond Ireland (+353) and Antigua (+1268)"]}

---
