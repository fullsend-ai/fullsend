# Conversation: silent-data-corruption x socratic-refinement

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of.

To start: can you walk me through how these customer phone numbers typically get into TaskFlow? For example, are they entered manually by your team, imported from a spreadsheet or another system, or synced automatically from somewhere? And do you have a sense of whether these numbers were ever correct in TaskFlow and then changed, or whether they may have come in wrong in the first place?

---

### reporter

They come in through a nightly sync from our Salesforce CRM. The numbers are definitely correct in Salesforce — we've spot-checked a bunch of them. So they were right at some point, but I think they've been getting overwritten by the sync with bad data. I'm not 100% sure when it started but I feel like it's been maybe the last couple weeks? Before that we never had this problem.

---

### triage-agent

That's really helpful — knowing the numbers are correct in Salesforce and that this started roughly two weeks ago narrows things down a lot.

Can you tell me more about what the corrupted numbers actually look like compared to the correct ones in Salesforce? For instance, if a correct number is `+1 (555) 867-5309`, what does the mangled version look like in TaskFlow — is it missing digits, missing the country code, has extra characters, or something else? If you could share a couple of before/after examples (with digits changed for privacy, of course), that would be really useful.

Also, do you recall whether anything changed around two weeks ago — a TaskFlow update, a change to your Salesforce setup, or maybe a change in how your phone numbers are formatted in Salesforce?

---

### reporter

Let me look at a couple examples... So we have a customer in Antigua, their number in Salesforce is something like +1268-555-1234 but in TaskFlow it shows up as +12-555-1234 or something like that — the country code gets cut short. Same thing with an Irish customer, +353 became +35. But our US and UK customers seem fine — like +1 and +44 numbers look correct. So it seems like it's only affecting certain countries?

As for what changed two weeks ago, I'm not sure on our end — we didn't change anything in Salesforce. I vaguely remember seeing something in the changelog about phone number formatting support being updated, maybe for international numbers? But I'm not 100% sure about that.

---

### triage-agent

[RESOLVED] {"title":"Salesforce sync truncates phone country codes longer than 2 digits","problem":"The nightly Salesforce-to-TaskFlow sync is corrupting phone numbers for countries whose dialing codes are longer than 2 digits. Country codes are being truncated to 2 digits (e.g., +1268 becomes +12, +353 becomes +35), while countries with 1-2 digit codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of ~2,000 customer records are affected.","root_cause_hypothesis":"A recent update to the phone number formatting/parsing logic (likely ~2 weeks ago, possibly referenced in the changelog as international number formatting support) introduced a bug that truncates the country code portion of phone numbers to a maximum of 2 digits. The parsing code likely assumes all country codes are 1-2 digits, discarding the remaining digits or misinterpreting where the country code ends and the subscriber number begins.","reproduction_steps":["Ensure a Salesforce contact record has a phone number with a 3+ digit country code (e.g., +1268-555-1234 for Antigua or +353-1-555-1234 for Ireland)","Run or wait for the nightly Salesforce sync to TaskFlow","Check the phone number stored in TaskFlow — the country code should be truncated to 2 digits"],"environment":"Salesforce-to-TaskFlow nightly sync integration; issue affects international phone numbers with 3+ digit country codes","severity":"high","impact":"Support team cannot reach ~50-100 international customers by phone. Data integrity issue that silently corrupts contact records on every sync cycle, meaning manual corrections are overwritten nightly.","recommended_fix":"1. Check the changelog/commits from ~2 weeks ago for changes to phone number parsing or international formatting. 2. Review the country code parsing logic in the Salesforce sync pipeline — look for hardcoded length limits or incorrect assumptions about country code length (ITU E.164 country codes range from 1-3 digits). 3. Fix the parser to correctly handle variable-length country codes. 4. Run a one-time re-sync or repair of affected records after the fix is deployed.","proposed_test_case":"Test the sync with phone numbers using 1-digit (+1), 2-digit (+44), and 3-digit (+353, +1268) country codes and verify all are stored correctly in TaskFlow without truncation. Include edge cases like +1 (NANP) vs +1268 (Antigua, which shares the +1 prefix but has a longer code).","information_gaps":["Exact version or commit that introduced the phone formatting change (~2 weeks ago)","Whether the sync pipeline has intermediate transformation steps between Salesforce and TaskFlow beyond the formatter"]}

---
