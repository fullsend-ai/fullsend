# Conversation: silent-data-corruption x superpowers-brainstorming

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely worth tracking down.

How are customer phone numbers getting into TaskFlow? For example:

1. **Manual entry** — your team types them into a form in the app
2. **Bulk import** — you imported them from a CSV/spreadsheet
3. **Integration/sync** — they come in automatically from another system (e.g., a CRM or billing platform)
4. **A mix of the above**

This will help us narrow down where the corruption is happening.

---

### reporter

It's option 3 — we have a nightly sync set up with our Salesforce CRM. It runs automatically and pulls in customer data including phone numbers. We've been using this integration for a while and it's generally been reliable, so this is surprising.

---

### triage-agent

That's helpful, thanks. Do you have a sense of when the bad numbers started appearing?

1. **Recently** — the numbers were correct before and changed after a specific sync (if so, roughly when?)
2. **Gradually** — you're not sure when it started, but the bad data has been accumulating over time
3. **From the beginning** — some records may have always been wrong, and your team only just noticed

Also — are the affected numbers all in a particular format, like international numbers or numbers with extensions? That could point to a parsing issue in the sync.

---

### reporter

I think it's been about two weeks, maybe a little more. The numbers were definitely correct before that — we use them regularly for customer outreach so we would have noticed sooner if it was a long-standing issue. It seems like it's been getting worse over time though, like more records are affected each day.

As for the format — good question, I think the affected ones are all international numbers. We have customers in a bunch of countries. The US and UK numbers seem fine, but I noticed the ones from places like Ireland and some Caribbean countries look wrong. The country code part looks cut off or something. I'm not 100% sure that's the exact pattern but that's what stood out to me when I was looking through them.

---

### triage-agent

Thanks — the pattern you're describing is really helpful. Here's what I think is happening:

It sounds like the sync is truncating or mishandling country codes that are longer than 1–2 digits. US (+1) and UK (+44) are short codes and come through fine, but countries like Ireland (+353) have 3-digit codes, and those are getting mangled.

To confirm this theory, could you grab one or two examples of a corrupted number and what it *should* be? For instance, if an Irish customer's number should be `+353 1 234 5678`, what does TaskFlow actually show — something like `+35 1 234 5678` or `3 1 234 5678` or something else entirely?

That'll tell us exactly where the truncation is happening and point us to the right spot in the sync code.

---

### reporter

Let me check... OK so I pulled up one of our Irish customers. Their number in Salesforce is +353 1 234 5678 but in TaskFlow it shows as +35 1 234 5678 — so yeah it looks like the country code got chopped. I also checked one of our Antigua customers, their number should start with +1268 but in TaskFlow it just shows +12 at the beginning. So it does look like it's cutting off the country code after 2 digits. The US numbers with just +1 are all fine though.

---

### triage-agent

[RESOLVED] {"title":"Salesforce sync truncates phone country codes longer than 2 digits","problem":"The nightly Salesforce-to-TaskFlow customer sync is corrupting international phone numbers by truncating country calling codes to 2 digits. Countries with 1-2 digit codes (US +1, UK +44) are unaffected, but countries with 3+ digit codes (Ireland +353, Antigua +1268) are mangled. Roughly 50-100 of ~2,000 customer records are affected and the number grows with each nightly sync run.","root_cause_hypothesis":"A change introduced approximately two weeks ago in the phone number parsing or storage path of the Salesforce sync is applying a 2-character limit to the country code portion of international phone numbers. This is likely a field-length constraint, substring operation, or regex pattern that assumes country codes are at most 2 digits.","reproduction_steps":["Identify a customer record in Salesforce with a country code longer than 2 digits (e.g., Ireland +353 1 234 5678)","Wait for (or manually trigger) the nightly Salesforce sync","Check the same customer record in TaskFlow — the country code will be truncated to 2 digits (e.g., +35 1 234 5678)"],"environment":"Salesforce CRM → nightly sync integration → TaskFlow (production). Issue began approximately two weeks ago.","severity":"high","impact":"Support and outreach teams cannot reach international customers with 3+ digit country codes. Data corruption worsens with each nightly sync run, affecting an increasing number of records. Currently 50-100 records affected out of ~2,000.","recommended_fix":"1. Check git history for changes to the Salesforce sync code from ~2 weeks ago, focusing on phone number parsing, formatting, or storage. 2. Look for a substring, field-length, or regex constraint that limits country codes to 2 characters. 3. Fix the parsing to accept country codes of 1-3 digits (per the ITU E.164 standard). 4. Run a one-time remediation to re-sync affected records from Salesforce to restore correct phone numbers.","proposed_test_case":"Unit test the phone number parser with country codes of varying lengths: +1 (US, 1 digit), +44 (UK, 2 digits), +353 (Ireland, 3 digits), +1268 (Antigua, 4-digit with shared +1 prefix). Assert that full country codes and subscriber numbers are preserved without truncation.","information_gaps":["Exact code change or deployment that introduced the bug ~2 weeks ago (developer can find via git history)","Whether the truncation happens during parsing, transmission, or storage (developer can trace through the sync pipeline)"]}

---
