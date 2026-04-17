# Conversation: silent-data-corruption x socratic-refinement

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for flagging this — data integrity issues like this are definitely something we want to get to the bottom of.

To help me understand the full picture: can you walk me through how these customer phone numbers typically get into TaskFlow? For example, are they entered manually by your team, imported from a spreadsheet or another system, or synced automatically from somewhere? And do you have a sense of whether these numbers were ever correct in TaskFlow, or whether they may have come in wrong from the start?

---

### reporter

They come in from our Salesforce CRM through a nightly batch import that syncs everything automatically. The numbers were definitely correct before — our support team has been using them for months without issues. I think this started maybe two weeks ago? I'm not 100% sure on the exact date but it's been recent. The weird thing is not all numbers are affected, just some of them.

---

### triage-agent

That's really helpful context — a nightly Salesforce sync that was working fine until recently narrows things down a lot.

Can you tell me more about what the wrong numbers actually look like? For instance, if a correct number is something like (555) 123-4567, what does the mangled version look like in TaskFlow — is it missing digits, missing the area code, has extra characters, or something else entirely? If you could share a couple of before/after examples (with digits changed for privacy, of course), that would be incredibly useful.

Also, do you recall anything that changed around two weeks ago — any updates to the Salesforce integration, changes to phone number formats in Salesforce, or updates to TaskFlow itself?

---

### reporter

Looking at a few examples, it seems like the country code part is getting cut short. Like we have customers in Ireland whose numbers should start with +353 but in TaskFlow they show as +35 followed by the rest of the number. And I think we have some Caribbean customers where the same thing is happening — the country code is chopped. But US and UK numbers seem fine, which is weird.

As for what changed, I'm not sure about any specific updates. I know our dev team did some work on the import process a couple weeks back — something about supporting international phone formats better, I think? You might want to check with them on the exact timing. The numbers in Salesforce are still correct, it's only the TaskFlow side that's wrong.

---

### triage-agent

[RESOLVED] {"title":"Nightly Salesforce import truncates country codes longer than 2 digits","problem":"Since approximately two weeks ago, the nightly Salesforce batch import is truncating phone number country codes that are longer than 2 digits. Numbers with 1- or 2-digit country codes (US +1, UK +44) import correctly, but 3-digit country codes (e.g., Ireland +353) are truncated to 2 digits (+35), corrupting approximately 50-100 of ~2,000 customer records. The source data in Salesforce remains correct.","root_cause_hypothesis":"A recent change to the import process (described as 'supporting international phone formats better') likely introduced a fixed-length assumption or off-by-one error in the country code parsing logic, limiting country codes to a maximum of 2 digits. This would explain why +1 and +44 are fine but +353 is truncated to +35.","reproduction_steps":["Identify the recent commit(s) to the Salesforce import process related to international phone format support (~2 weeks ago)","Set up a test Salesforce record with a 3-digit country code phone number (e.g., +353 1 234 5678 for Ireland)","Run the nightly import process (or its equivalent in a test environment)","Verify the imported phone number in TaskFlow — expect the country code to be truncated to 2 digits"],"environment":"Production nightly Salesforce-to-TaskFlow batch import. Affects international numbers with 3+ digit country codes. US and UK numbers unaffected.","severity":"high","impact":"Approximately 50-100 customer records have corrupted phone numbers, causing support team to reach wrong numbers. Data integrity issue affecting customer communication. Ongoing — each nightly sync may re-corrupt any manually fixed records.","recommended_fix":"1. Review the recent international phone format changes to the Salesforce import pipeline, specifically the country code parsing logic. Look for hardcoded length limits, substring operations, or regex patterns that assume country codes are at most 2 digits. 2. Fix the parser to handle 1-, 2-, and 3-digit country codes per the ITU E.164 standard. 3. Run a one-time remediation to re-import affected records from Salesforce to restore correct phone numbers.","proposed_test_case":"Unit test the phone number parser with representative country codes of each length: +1 (US, 1 digit), +44 (UK, 2 digits), +353 (Ireland, 3 digits), and +1-868 (Trinidad, +1 with 3-digit area code). Assert that the full country code and remaining digits are preserved exactly. Integration test: import a batch containing all three country code lengths and verify no truncation.","information_gaps":["Exact commit or PR that modified the import process — dev team can identify this from their git history around 2 weeks ago","Which specific Caribbean country codes are affected — would further confirm the 3-digit truncation pattern vs. +1-area-code Caribbean numbers","Whether the nightly import overwrites previously correct data on each run (determines if manual fixes are being re-corrupted)"]}

---
