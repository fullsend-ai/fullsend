# Conversation: silent-data-corruption x superpowers-brainstorming

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues are definitely worth investigating quickly.

To help narrow down the root cause, can you describe what the corrupted phone numbers look like? For example:

1. **Truncated** — digits are missing from the end (e.g., `+1 555-123` instead of `+1 555-123-4567`)
2. **Leading digits dropped** — the country code or area code is missing (e.g., `123-4567` instead of `555-123-4567`)
3. **Garbled/substituted** — wrong digits entirely, not just missing ones
4. **Formatting mangled** — the digits might be correct but symbols or spaces are wrong (e.g., `15551234567` instead of `+1 (555) 123-4567`)
5. Something else?

If you could paste one or two examples (redacted is fine — e.g., replace real digits with X's but keep the shape), that would be very helpful.

---

### reporter

Looking at a few examples, it seems like the country code part is getting cut short. Like we have customers in Ireland whose numbers should start with +353 but they show as +35 followed by the rest of the number. Similarly I think we had one from Antigua that should be +1268 but it's showing as +12. Meanwhile our US and UK customers with +1 and +44 seem fine. So it's kind of like option 1 — truncated — but specifically in the country code portion, not the end of the number.

---

### triage-agent

That's a very helpful pattern — it's clear the country code is being truncated to 2 characters, which explains why +1 (US) and +44 (UK) are fine but +353 (Ireland) becomes +35.

Do you know whether these numbers were ever correct in TaskFlow, or have they been wrong since they were first entered?

1. **They used to be correct** — something changed recently (e.g., a data migration, import, or software update) and the numbers got corrupted
2. **They've always been wrong** — these numbers were entered or imported incorrectly from the start
3. **Not sure** — you only recently noticed, so you can't tell when it started

This will help determine whether we're looking for a recent code/schema change that broke things, or a longer-standing issue with how phone numbers are stored.

---

### reporter

They were definitely correct before. I'm pretty sure this started around two weeks ago — that's roughly when our support team started getting wrong numbers when calling customers. We do a nightly import from our CRM (Salesforce) that syncs customer data into TaskFlow, so I'm guessing something changed in how that import handles the numbers. The numbers are still correct in Salesforce, it's only in TaskFlow that they're messed up.

---

### triage-agent

[RESOLVED] {"title":"Nightly Salesforce import truncates phone country codes to 2 characters","problem":"Customer phone numbers with country codes longer than 2 digits (e.g., +353 for Ireland, +1268 for Antigua) are being truncated to 2 characters during the nightly Salesforce-to-TaskFlow import. Country codes that are already 1–2 digits (+1, +44) are unaffected. Approximately 50–100 of ~2,000 customer records are impacted. The data remains correct in Salesforce.","root_cause_hypothesis":"A change introduced approximately two weeks ago in the Salesforce import pipeline is truncating the country code portion of phone numbers to 2 characters. This could be a schema change (e.g., a column defined as VARCHAR(2) or CHAR(2) for the country code), a parsing change that takes only the first 2 characters after the '+', or a validation/formatting change that assumes all country codes are at most 2 digits.","reproduction_steps":["Identify a Salesforce customer record with a 3+ digit country code (e.g., +353 for Ireland)","Verify the phone number is correct in Salesforce","Wait for or manually trigger the nightly import","Check the same customer record in TaskFlow — the country code should be truncated to 2 digits (e.g., +35 instead of +353)"],"environment":"TaskFlow production environment with nightly data sync from Salesforce CRM. Issue began approximately two weeks ago (late March 2026).","severity":"high","impact":"Support team cannot reach customers in countries with 3+ digit country codes. Approximately 50–100 records affected. Data corruption is ongoing — each nightly import re-corrupts any manually corrected numbers.","recommended_fix":"1. Check git history on the Salesforce import code for changes made ~2 weeks ago. 2. Look for any field-length constraint, substring operation, or format mask that limits the country code to 2 characters. 3. Fix the truncation so country codes of any valid length (1–3 digits per E.164) are preserved. 4. Re-run the import or write a one-time repair script to re-sync affected records from Salesforce.","proposed_test_case":"Import a customer record with a 3-digit country code (e.g., +353 1 234 5678 for Ireland) and verify the full number, including all country code digits, is stored correctly in TaskFlow. Include edge cases for 1-digit (+1), 2-digit (+44), and 3-digit (+353) country codes.","information_gaps":["Exact date the corruption started (reporter estimates ~2 weeks ago)","Whether the nightly import overwrites previously correct values on every run (likely, which means manual corrections would be lost)","The specific technology stack of the import pipeline (ETL tool, custom script, etc.)"]}

---
