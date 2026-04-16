# Conversation: silent-data-corruption x structured-triage

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

To help us investigate, could you share a few concrete examples of what the mangled phone numbers look like? For instance, if the correct number should be `(555) 123-4567`, what does TaskFlow show instead? Even 2-3 examples of the before/after (what the number should be vs. what's stored) would help us identify the pattern — whether digits are being dropped, characters are being added, formatting is being changed, etc.

Also, do you know where these numbers were entered — through the TaskFlow UI, via an import/CSV upload, or through an API?

---

### reporter

Sure, let me pull up a couple examples. We have a customer in Ireland whose number should be +353 1 234 5678 but in TaskFlow it shows as +35 1 234 5678 — the country code got cut short. Another one from Antigua, should be +1268 555 1234 but it's showing +12 555 1234. Meanwhile our US customers with +1 numbers all look fine, and UK ones with +44 seem okay too.

The numbers come in through a nightly batch import we have set up from our CRM (Salesforce). We don't usually enter phone numbers manually in TaskFlow.

---

### triage-agent

Thanks for those examples — they're very helpful. The pattern is quite clear: it looks like country codes longer than 2 digits are being truncated to exactly 2 digits (+353 → +35, +1268 → +12), while 1- and 2-digit codes like +1 and +44 come through fine.

To help our developers track down exactly where this truncation is happening, could you share some environment details?

- What version of TaskFlow are you running?
- How is the nightly batch import configured — is it using TaskFlow's built-in Salesforce integration, a custom script calling our API, or something else?
- Has this import been running for a while without issues, or was it recently set up or updated?

---

### reporter

We're on TaskFlow v2.3.1 I believe. The nightly import uses the built-in Salesforce integration — we set it up through the TaskFlow admin panel, nothing custom on our end. It's been running for months without any problems. Actually, now that you mention it, I think this started maybe two weeks ago? Our support team only caught it recently but when I looked back at the records, the ones that are wrong seem to have been updated over the last couple weeks. Before that everything was fine. Not sure if something changed on your end around that time.

---

### triage-agent

[RESOLVED] {"title":"Phone country codes longer than 2 digits truncated during Salesforce batch import","problem":"The built-in Salesforce integration's nightly batch import is truncating phone number country codes to a maximum of 2 digits. Numbers with 1- or 2-digit country codes (+1, +44) are unaffected, but 3-digit codes like +353 (Ireland) become +35 and 4-digit codes like +1268 (Antigua) become +12. An estimated 50-100 of ~2,000 customer records are affected.","root_cause_hypothesis":"A recent change (likely in TaskFlow v2.3.x or a migration applied around 2 weeks ago) introduced a field length constraint or parsing bug that limits the country code portion of international phone numbers to 2 characters. This could be a database column width change, a validation regex update, or a parsing function that splits on the wrong boundary when extracting the country code during import normalization.","reproduction_steps":["Set up the built-in Salesforce integration via the TaskFlow admin panel","Ensure Salesforce contains contact records with phone numbers using 3+ digit country codes (e.g., +353 1 234 5678 for Ireland)","Run the nightly batch import (or trigger it manually if possible)","Check the imported phone numbers in TaskFlow — country codes longer than 2 digits will be truncated to 2 digits"],"environment":"TaskFlow v2.3.1, built-in Salesforce integration configured through admin panel, nightly batch import schedule","severity":"high","impact":"50-100 customer contact records have corrupted phone numbers, preventing support staff from reaching customers. Data corruption is ongoing with each nightly import, so the number of affected records will grow. Affects any organization using the Salesforce integration with international contacts outside +1/+44 regions.","recommended_fix":"1. Check recent changes (last ~2 weeks) to the phone number parsing/normalization code in the Salesforce import pipeline, especially any country code extraction logic. 2. Look for database schema migrations that may have altered column widths on phone-related fields. 3. Review any validation regex changes that constrain country code length. 4. After fixing, provide a data remediation path — re-import affected records or re-sync from Salesforce to restore correct numbers.","proposed_test_case":"Import phone numbers with country codes of varying lengths (+1, +44, +353, +1268, +880) via the Salesforce integration and verify all digits are preserved exactly. Include edge cases: 1-digit (+1), 2-digit (+44), 3-digit (+353), and 4-digit (+1268) country codes.","information_gaps":["Exact TaskFlow release or deployment change that occurred ~2 weeks ago that may have introduced the regression","Whether the truncation happens at import time (data written incorrectly) or at display time (data stored correctly but rendered incorrectly)","Import logs from the Salesforce integration that might show warnings or errors during processing"]}

---
