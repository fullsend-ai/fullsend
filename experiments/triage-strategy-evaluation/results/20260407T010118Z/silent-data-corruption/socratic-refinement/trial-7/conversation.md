# Conversation: silent-data-corruption x socratic-refinement

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for flagging this — data integrity issues like this are definitely worth investigating carefully.

To help me understand what's going on, can you walk me through how these customer phone numbers originally got into TaskFlow? For example, were they entered manually by your team, imported from a spreadsheet or another system, or migrated from a previous tool? And do you have a sense of whether the affected numbers were all added around the same time, or if this is spread across records created at different points?

---

### reporter

The phone numbers come from our CRM — Salesforce. We have a nightly batch import that syncs customer data into TaskFlow. The records have been building up over time, but I think the bad numbers are a more recent problem. Our support team only started noticing it in the last couple weeks. Before that everything seemed fine, so I don't think it's an issue with how the records were originally created. It's like something changed recently that's messing up certain numbers during the sync.

---

### triage-agent

That's really helpful — the nightly Salesforce sync is a strong lead. I'd like to follow that thread a bit further.

When you say the numbers look "truncated or mangled," can you share a couple of examples of what a correct number looks like versus what's ending up in TaskFlow? For instance, are digits being cut off from the beginning or end, are formatting characters (dashes, parentheses, country codes) being stripped or added, or is it something else entirely?

Also, you mentioned something may have changed recently — do you know if there were any updates to the sync job, the TaskFlow configuration, or even the phone number format on the Salesforce side around the time your support team started noticing the problem?

---

### reporter

Yeah so for example, we have some customers in Ireland and their numbers should start with +353 but in TaskFlow they're showing up as +35 and then the rest of the number. We also have a few customers in the Caribbean — I think Antigua — and those are messed up too, the country code is getting chopped. But our US and UK customers seem fine, their numbers look right. So it's like the beginning of the number is getting cut short, specifically the country code part.

As for changes, I'm not 100% sure but I think one of our developers made some update to how phone numbers are handled — something about supporting international formats? That was maybe two weeks ago, which lines up with when the support team started getting wrong numbers. I'd have to check with the dev team on the exact details though.

---

### triage-agent

Thanks for those examples — that's a very clear pattern emerging. Let me share what I'm piecing together, and you can tell me if this sounds right:

It looks like the recent change to support international phone number formats may have introduced a bug where country codes longer than two digits are being truncated to two digits. Ireland's +353 becomes +35, and I'd guess Antigua's +1-268 area code is getting mangled in a similar way. Meanwhile US (+1) and UK (+44) numbers come through fine because their country codes are already 1-2 digits.

Does that match what you're seeing? And would you be able to check with the dev team on what specifically was changed about phone number handling — for example, was it in the import/sync job itself, or in a shared phone number parsing or validation library within TaskFlow? Knowing where that change lives would help us point a developer straight to the right code.

---

### reporter

Yeah that sounds exactly right — the US and UK numbers are fine but anything with a longer country code is getting chopped to two digits. That matches what we're seeing.

I don't know the technical details of what was changed, but I'm pretty sure it was something in the import/sync process itself — like the part that cleans up or normalizes the phone numbers as they come in from Salesforce. The numbers in Salesforce are all correct, it's only after they get pulled into TaskFlow that they're wrong. I'd have to ask the dev who made the change for specifics though. I think it was meant to be an improvement to handle international formats better, which is kind of ironic.

---

### triage-agent

[RESOLVED] {"title":"Phone number country codes truncated to 2 digits during Salesforce nightly sync after recent international format change","problem":"Customer phone numbers with country codes longer than 2 digits (e.g., Ireland +353, Caribbean nations) are being truncated to 2 digits during the nightly Salesforce-to-TaskFlow batch import. Approximately 50-100 of ~2,000 records are affected. US (+1) and UK (+44) numbers are unaffected.","root_cause_hypothesis":"A recent change (~2 weeks ago) to phone number normalization/parsing in the import sync job likely hardcodes or truncates the country code field to a maximum of 2 characters. This would explain why +1 and +44 pass through correctly while +353 becomes +35.","reproduction_steps":["Ensure a Salesforce record exists with a phone number using a 3-digit country code (e.g., +353 1 234 5678 for Ireland)","Run the nightly batch import (or trigger it manually)","Inspect the resulting phone number in TaskFlow — the country code will be truncated to 2 digits (+35 1 234 5678)"],"environment":"Salesforce-to-TaskFlow nightly batch sync job; issue is in the import/normalization layer, not in Salesforce data or TaskFlow storage","severity":"high","impact":"Support team cannot reach affected international customers (~50-100 records). Data integrity issue that silently corrupts contact information on every sync run, meaning corrected records would be re-corrupted nightly until fixed.","recommended_fix":"Find the recent commit (~2 weeks ago) that modified phone number normalization in the sync/import job. Look for code that parses or extracts the country code and likely slices or substrings it to 2 characters. Fix to handle variable-length country codes (1-3 digits per E.164). After fixing, re-sync affected records from Salesforce to restore correct numbers.","proposed_test_case":"Unit test the phone number normalization function with country codes of varying lengths: +1 (1 digit, US), +44 (2 digits, UK), +353 (3 digits, Ireland), +1-268 (1 digit + area code, Antigua). Assert that the full country code and number are preserved in each case.","information_gaps":["Exact commit or code location of the recent phone number handling change (dev team can identify via git history)","Whether the truncation also affects the Antigua numbers' area code or just the country code portion","Whether already-corrupted records will self-heal on re-sync once the bug is fixed, or if a one-time data repair is needed"]}

---
