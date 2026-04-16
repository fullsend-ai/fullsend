# Conversation: silent-data-corruption x omc-deep-interview

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

Could you share a couple of examples of what the corrupted phone numbers look like compared to what they should be? For instance, are the numbers consistently truncated to a certain length, are digits being dropped from the beginning or end, or does the mangling look more random? (Feel free to redact the actual digits if needed — the pattern is what matters most.)

---

### reporter

Yeah so I pulled up a few examples. It looks like the country code part is getting cut short. For example, we have a customer in Ireland whose number should start with +353 but in TaskFlow it shows as +35 followed by the rest of the digits. Another one is an Antigua number that should be +1268 but it's showing as +12. The weird thing is that our US and UK customers seem fine — their +1 and +44 numbers look correct. So it seems like it's only affecting certain country codes, specifically the longer ones? The digits after the country code seem to be there, it's just the country code itself that's getting chopped.

---

### triage-agent

That pattern is really helpful — it definitely looks like country codes are being truncated to a fixed number of characters. To help us pinpoint where this is happening: how are these customer phone numbers getting into TaskFlow? Are they entered manually through the UI, imported via CSV/bulk upload, or coming in through an API or integration? And did you notice this recently — for example, after an update or migration — or is it possible this has been happening for a while and the support team just caught it now?

---

### reporter

They come in through a nightly batch import — we sync from our Salesforce CRM automatically. Nobody's entering these manually. As for timing, I think it started maybe two weeks ago? I'm not 100% sure on the exact date but it was definitely recent because these numbers were correct before. I feel like something changed around that time but I don't know what exactly on your end. We just noticed it this week when support started getting wrong numbers.

---

### triage-agent

That timing is really useful context, thank you. One key thing that would help us narrow this down: could you check whether those same phone numbers (the Ireland +353 one, the Antigua +1268 one) are still stored correctly in Salesforce right now? If they look fine in Salesforce but corrupted in TaskFlow, that tells us the problem is in the import/parsing layer on our side. Also — and I know you said you're not sure what changed — do you happen to know if the phone number *format* coming out of Salesforce changed at all around that time? For example, did Salesforce start sending numbers in E.164 format when it previously sent them differently, or vice versa?

---

### reporter

Yeah, the numbers are definitely still correct in Salesforce — I just double-checked a couple of them and they look fine over there. So it's something happening when they come into TaskFlow. As for the format from Salesforce, I don't think anything changed on that side. We haven't made any changes to our Salesforce setup recently. The numbers have always been in the same format as far as I know, with the + and country code. So I'm pretty sure the issue is on the TaskFlow side of things.

---

### triage-agent

Thanks for confirming — that definitively points to something on our import/parsing side.

One more thing that would really help us track this down: is the corruption still happening with each nightly import, or did it happen once and the bad data has just been sitting there since? For example, if you look at a record that was imported or re-synced last night, does a longer country code like +353 still come in truncated? Knowing whether this is ongoing versus a one-time corruption event will help our team figure out exactly where to look.

---

### reporter

Yeah, it's still happening. Every night when the sync runs, any records that get touched come in with the truncated country codes. I actually tried re-importing one of the Ireland customer records manually through the same batch process and it came in wrong again with +35 instead of +353. So it's definitely ongoing, not a one-time thing. The nightly import keeps overwriting the numbers with the bad versions.

---

### triage-agent

[RESOLVED] {"title":"Batch import truncates phone number country codes longer than 2 characters","problem":"Phone numbers imported from Salesforce via the nightly batch sync have their country codes truncated to what appears to be 2 characters. Numbers with short country codes (+1 US, +44 UK) are unaffected, while longer country codes are corrupted (e.g., Ireland +353 becomes +35, Antigua +1268 becomes +12). The remaining digits after the country code are preserved. Approximately 50–100 of ~2,000 customer records are affected.","root_cause_hypothesis":"A change introduced roughly two weeks ago in the phone number import/parsing pipeline is truncating the country code portion of phone numbers to a fixed length (likely 2 characters). This could be a field-width constraint introduced in a database migration, a parsing regex or substring operation that assumes all country codes are 1–2 digits, or a change to the phone number normalization/validation logic. Country codes are 1–3 digits per the E.164 standard, so a 2-character limit would silently corrupt 3-digit codes.","reproduction_steps":["Identify a Salesforce contact record with a 3-digit country code (e.g., Ireland +353)","Verify the phone number is correct in Salesforce","Trigger the batch import process for that record (or wait for the nightly sync)","Check the resulting phone number in TaskFlow — the country code should be truncated to 2 digits"],"environment":"Production environment. Nightly batch import from Salesforce CRM to TaskFlow. Issue began approximately two weeks ago (late March 2026). No changes were made on the Salesforce side.","severity":"high","impact":"50–100 customer records have corrupted phone numbers. Support team is unable to reach affected customers by phone. The corruption is ongoing — every nightly sync overwrites records with bad data, so manual corrections are overwritten. Primarily affects customers in countries with 3-digit country codes.","recommended_fix":"1. Review git history for the phone number import/parsing code from roughly 2–3 weeks ago to identify the change that introduced the truncation. 2. Look for field-width constraints, substring operations, or regex patterns that limit the country code to 2 characters. 3. Fix the parsing to support country codes of 1–3 digits per the E.164 standard. 4. After deploying the fix, re-run the batch import for all affected records to restore correct phone numbers from Salesforce.","proposed_test_case":"Import phone numbers with country codes of varying lengths — 1 digit (+1 US), 2 digits (+44 UK), and 3 digits (+353 Ireland, +880 Bangladesh) — and verify all are stored correctly without truncation. Include edge cases like +1268 (Antigua, which uses the NANP +1 prefix but has a longer full dialing code).","information_gaps":["Exact date the issue started (reporter estimates ~2 weeks ago but is uncertain)","Exact character length the country code is being truncated to (strongly suspected to be 2 based on examples, but not confirmed across a broader sample)","Whether the issue is in the parsing code, a DB column constraint, or a validation rule — requires code inspection"]}

---
