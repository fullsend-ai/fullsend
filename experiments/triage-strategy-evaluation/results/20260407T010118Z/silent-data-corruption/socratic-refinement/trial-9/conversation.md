# Conversation: silent-data-corruption x socratic-refinement

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for flagging this — data integrity issues like this are definitely worth digging into.

To help me understand the full picture: can you walk me through how customer phone numbers typically get into TaskFlow? For example, are they entered manually by your team, imported from a spreadsheet or another system, or pulled in through an API? And do you have a sense of whether these numbers were correct at some point and then changed, or whether they may have been wrong from the moment they were entered?

---

### reporter

They come in through a nightly batch import from our Salesforce CRM. We have an automated sync that runs every night and updates the customer records in TaskFlow. The numbers were definitely correct at some point — we've been using this system for a while and this is a recent problem. I think it started maybe two weeks ago? Our support team only noticed last week when they started getting wrong numbers, but looking back at the records it seems like it's been going on a bit longer than that.

---

### triage-agent

That's really helpful context — a nightly sync that recently started producing bad data is a strong lead.

I'd love to understand two related things: First, can you show me an example or two of what the mangled numbers look like compared to what they should be? For instance, if a number should be `(555) 123-4567`, what does it actually look like in TaskFlow — is it missing digits, missing the area code, has extra characters, or something else?

And second, do you know if anything changed around two weeks ago — an update to the sync job, a change in Salesforce field configuration, a TaskFlow upgrade, or anything like that? Even something that seemed unrelated could be a clue.

---

### reporter

Yeah, so for example we have a customer in Ireland whose number should be +353 1 234 5678 but in TaskFlow it shows up as +35 1 234 5678 — it's like the country code got cut short. Another one I noticed was a Caribbean customer, their number starts with +1268 but it got chopped to +12 something. The weird thing is our US and UK customers seem fine — their numbers look correct. It's only certain international ones that are messed up.

As for changes, I'm not 100% sure but I think one of the developers mentioned updating something related to how phone numbers are handled — maybe to support international formats better? That was roughly around the time this started. I don't know the exact details though, you'd have to check with the dev team on what was deployed around then.

---

### triage-agent

[RESOLVED] {"title":"International phone numbers with 3+ digit country codes are truncated during Salesforce nightly sync","problem":"The nightly batch import from Salesforce into TaskFlow is truncating international phone number country codes to 2 digits. Approximately 50-100 of 2,000 customer records are affected. Numbers with country codes of 1-2 digits (e.g., US +1, UK +44) are unaffected, but longer country codes are mangled (e.g., Ireland +353 → +35, Caribbean +1268 → +12).","root_cause_hypothesis":"A code change deployed approximately two weeks ago — reportedly related to international phone number format handling — introduced a bug that truncates the country code portion of phone numbers to a maximum of 2 digits during the sync/import parsing step.","reproduction_steps":["Identify a Salesforce customer record with a country code of 3+ digits (e.g., +353 for Ireland)","Verify the phone number is correct in Salesforce","Wait for (or manually trigger) the nightly batch sync to TaskFlow","Check the resulting phone number in TaskFlow — the country code should be truncated to 2 digits"],"environment":"Nightly batch import pipeline from Salesforce CRM to TaskFlow. Specific sync job configuration and runtime environment details to be confirmed by the dev team.","severity":"high","impact":"Support team cannot reach ~50-100 international customers by phone. Data integrity of customer contact records is compromised, and the problem grows each night as the sync re-processes records. Domestic US and UK customers are unaffected.","recommended_fix":"Review commits from approximately 2 weeks ago related to phone number parsing or international format handling. Look for logic that extracts or validates the country code — it likely assumes or enforces a max length of 2 characters. The fix should support country codes of 1-3 digits per the ITU E.164 standard. After fixing, run a one-time re-sync or corrective import to restore the ~50-100 affected records from Salesforce source data.","proposed_test_case":"Unit test the phone number parsing function with country codes of varying lengths: +1 (US, 1 digit), +44 (UK, 2 digits), +353 (Ireland, 3 digits), +1268 (Antigua, 4-digit E.164 including trunk prefix). Assert that all are preserved without truncation through the sync pipeline.","information_gaps":["Exact commit or PR that changed phone number handling ~2 weeks ago","Whether the truncation happens during parsing on import, during storage, or during display/formatting","Whether already-corrupted records need a backfill from Salesforce or can self-heal on the next sync after the fix"]}

---
