# Conversation: silent-data-corruption x superpowers-brainstorming

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are important to get right.

To help narrow down the cause, can you tell me how these customer phone numbers typically get into TaskFlow?

1. **Manual entry** — your team types them into a form in the app
2. **Bulk import** — you uploaded a CSV or similar file (recently or in the past)
3. **Integration/sync** — they come in automatically from another system (e.g., a CRM or billing platform)
4. **A mix of the above**

Also, do you have a sense of whether the affected numbers were all added or updated around the same time, or are they spread across different time periods?

---

### reporter

They come in through an integration — we have a nightly sync from Salesforce that pulls in customer data. The numbers were fine before, but I think the bad ones have been showing up over the last couple weeks maybe? I'm not 100% sure exactly when it started but it definitely wasn't always like this. Our support team only noticed recently when they started getting wrong numbers for some international customers.

---

### triage-agent

That's really helpful — the Salesforce nightly sync and the international customer detail are important clues.

Can you tell me what the corrupted numbers look like compared to what they should be? For example:

1. **Truncated** — the number is missing its leading digits (e.g., `+44 20 7946 0958` became `20 7946 0958` or `7946 0958`)
2. **Missing the `+` or country code** — the number lost its international prefix
3. **Scrambled/garbled** — digits are rearranged or replaced with unexpected characters
4. **Something else**

If you can share a before/after example (with a fake number is fine), that would help pinpoint exactly where the data is getting mangled.

---

### reporter

I think they're truncated — like the country code part is getting cut short. I don't have the exact numbers in front of me but from what our support team said, it's like a number that should start with +353 (we have some Irish customers) ends up starting with +35 instead, so the rest of the number is off. The US and UK numbers seem fine though, it's mostly the ones with longer country codes that are messed up. I haven't noticed any scrambled characters or anything like that, just the truncation.

---

### triage-agent

[RESOLVED] {"title":"Salesforce sync truncates international phone country codes longer than 2 digits","problem":"The nightly Salesforce-to-TaskFlow customer sync is truncating international phone number country codes that are longer than 2 digits. For example, Irish numbers with country code +353 are stored as +35, making them invalid. An estimated 50-100 of ~2,000 customer records are affected. US (+1) and UK (+44) numbers are not affected because their country codes are 1-2 digits.","root_cause_hypothesis":"A recent change (within the last ~2 weeks) in the Salesforce sync import pipeline is truncating the country code portion of phone numbers to 2 digits. This could be a database column length constraint, a parsing/formatting bug in the sync code that assumes all country codes are 1-2 digits, or a regex/split operation that mishandles 3-digit country codes like +353, +354, +355, etc.","reproduction_steps":["Identify a customer record in Salesforce with a 3-digit country code (e.g., +353 for Ireland, +886 for Taiwan)","Verify the phone number is correct and complete in Salesforce","Wait for the nightly sync to run (or trigger it manually)","Check the same record in TaskFlow — the country code should be truncated to 2 digits","Compare against a US or UK record to confirm those are unaffected"],"environment":"Salesforce-to-TaskFlow nightly sync integration; affects international phone numbers with 3+ digit country codes","severity":"high","impact":"Support team cannot reach ~50-100 international customers by phone. Data integrity issue that worsens with each nightly sync as more records are corrupted. Existing corrupted records will need a backfill repair.","recommended_fix":"1. Check git/deploy history for changes to the Salesforce sync code or phone number schema in the last 2-3 weeks. 2. Review the phone number parsing/storage logic — look for field length constraints, substring operations, or regex patterns that assume country codes are at most 2 digits. 3. Fix the parsing to handle country codes of 1-3 digits (per ITU E.164 standard). 4. Run a one-time backfill by re-syncing affected records from Salesforce to repair corrupted numbers. 5. Add a validation check that flags phone numbers whose country code prefix doesn't match a known valid code.","proposed_test_case":"Create test phone numbers with country codes of varying lengths (+1, +44, +353, +1684) and run them through the sync import pipeline. Assert that all numbers are stored exactly as provided, with no truncation. Include edge cases like +1 (1-digit), +44 (2-digit), +353 (3-digit), and numbers with/without the + prefix.","information_gaps":["Exact date the truncation started (reporter estimates ~2 weeks but is unsure)","Whether a specific deployment or config change to the sync coincided with the onset","Exact before/after phone number examples from the database (reporter described the pattern but didn't have records in front of them)"]}

---
