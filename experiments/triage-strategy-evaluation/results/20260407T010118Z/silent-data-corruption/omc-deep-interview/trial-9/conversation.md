# Conversation: silent-data-corruption x omc-deep-interview

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

Could you share a couple of examples of what the mangled phone numbers look like compared to what they should be? For instance, are digits missing from the beginning, the end, or are they scrambled in some other way? And do you know roughly when these records were created or last updated — were they entered recently, imported in bulk, or have they been in the system for a while?

---

### reporter

Yeah, so looking at a few examples — we have a customer in Ireland whose number should start with +353 but in TaskFlow it shows as +35 followed by the rest of the digits. Similarly, we have a customer from Antigua and Barbuda (+1268) and their number got cut down to just +12 something. But I checked and our US customers (+1) and UK customers (+44) seem fine. So it looks like the country code part is getting chopped off for some numbers but not others.

As for timing, I think this started maybe 2 weeks ago? The numbers were definitely correct before that. These records come in through our nightly import from Salesforce — we don't usually enter them manually.

---

### triage-agent

That pattern is really helpful — it looks like country codes longer than 2 digits are getting truncated to 2. That points pretty strongly at the nightly Salesforce import.

Do you know if anything changed in the import process around 2 weeks ago? For example, was there a TaskFlow update, a change to the import configuration or field mappings, or any changes on the Salesforce side (like a field format change or API version bump)? Also, are new records still coming in with truncated numbers, or did it only affect a specific batch?

---

### reporter

I'm not sure about the exact details, but I think one of the developers mentioned they made some update to how phone numbers are processed — something about supporting international formats? That was around 2 weeks ago, which lines up with when we started seeing the issue. I don't think anything changed on the Salesforce side.

And yes, it's still happening. Every morning when the sync runs, I think it's overwriting the numbers again. We actually fixed a couple manually but they got mangled again the next day after the import ran. That's when we realized it was ongoing and not just a one-time thing.

---

### triage-agent

That really narrows it down — a recent change to phone number processing that coincides with when the issue started is a strong lead.

Could you find out from that developer what the change was — ideally a pull request number, commit, or at least which file or module was modified? That would let us go straight to the root cause. If you're not sure, even the developer's name so we can search recent commits would help.

Also, one quick clarification: you mentioned +353 became +35 and +1268 became +12. Do you happen to have an example with a 3-digit country code (like +353 Ireland) where you can confirm the stored number is exactly 2 digits for the code — not, say, the whole number being cut to a fixed length? I want to make sure we're chasing the right pattern.

---

### reporter

I don't know the PR number or anything like that — I'm not really on the development side. I think it might have been someone named Dave or Daniel, but honestly I'm not 100% sure. You'd probably have better luck just searching recent commits related to phone numbers or the import process.

As for the pattern — yes, I just double-checked. The Ireland customer's number should be +353 1 234 5678 and in TaskFlow it's stored as +35 1 234 5678. So it's definitely just the country code that's getting cut to 2 digits. The rest of the number after the country code looks correct. Same thing with the Antigua one — the +1268 became +12, but the local part of the number is intact.

---

### triage-agent

[RESOLVED] {"title":"Nightly Salesforce import truncates phone country codes longer than 2 digits","problem":"Customer phone numbers imported from Salesforce via the nightly sync are having their country codes truncated to exactly 2 digits. Numbers with 1- or 2-digit country codes (e.g., +1 US, +44 UK) are unaffected, but numbers with 3- or 4-digit country codes (e.g., +353 Ireland, +1268 Antigua) are corrupted. The local portion of the number after the country code is preserved correctly. The issue is ongoing — manually corrected numbers are re-corrupted on the next import cycle.","root_cause_hypothesis":"A code change made approximately 2 weeks ago to support international phone number formats introduced a bug that hard-codes or truncates the country code field to a maximum of 2 characters. This is likely in the phone number parsing/normalization logic applied during the Salesforce import pipeline. A developer reportedly made changes to 'how phone numbers are processed' around the time the issue began.","reproduction_steps":["Identify the nightly Salesforce import job/process","Ensure a Salesforce record exists with a phone number using a country code longer than 2 digits (e.g., +353 1 234 5678 for Ireland)","Run the import process (or wait for the nightly sync)","Check the imported record in TaskFlow — the country code should be truncated to 2 digits (+35 1 234 5678)"],"environment":"Production environment. Nightly Salesforce-to-TaskFlow sync process. Approximately 2,000 customer records total, 50-100 affected.","severity":"high","impact":"Support team cannot reach affected customers by phone. Data corruption is ongoing and overwrites manual corrections daily. Affects all customers with country codes longer than 2 digits (international customers outside the NANP +1 zone and countries with 2-digit codes).","recommended_fix":"Search recent commits (approximately 2 weeks old) related to phone number parsing, formatting, or international number support in the Salesforce import pipeline. Look for a hard-coded length limit, substring operation, or field width constraint on the country code portion — likely something like `country_code[:2]` or a VARCHAR(2) column or similar 2-character truncation. Fix the parsing logic to accommodate country codes of 1-4 digits per the ITU E.164 standard. After fixing, run a one-time re-import or correction of the ~50-100 affected records.","proposed_test_case":"Unit test the phone number parsing function with country codes of varying lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4 digits). Assert that the full country code is preserved in each case and the complete E.164 number is stored without truncation.","information_gaps":["Exact commit or PR that introduced the phone number processing change","Which specific file/module contains the parsing logic","Whether the truncation happens in application code or at the database schema level (e.g., a column width constraint)"]}

---
