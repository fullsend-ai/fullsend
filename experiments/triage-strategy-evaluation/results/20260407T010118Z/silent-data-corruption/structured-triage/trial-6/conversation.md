# Conversation: silent-data-corruption x structured-triage

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

To start narrowing this down: can you describe what the phone numbers *should* look like versus what you're seeing? For example, are the numbers missing country codes, losing digits at the end, or showing unexpected characters? And do you know whether these numbers were entered manually, imported from another system, or created through an API?

A couple of before/after examples (even redacted ones like `+1-555-XXX-XXXX` → `555-XXX-XX`) would be very helpful.

---

### reporter

So the numbers are international ones — they seem to have their country codes cut short. For example, we have customers in Ireland and a few Caribbean countries. An Irish number that should start with +353 looks like it starts with +35 instead, and I think one of our Antigua contacts whose number should be +1268 is showing as +12 or something like that. US and UK numbers seem fine though, those still look correct.

These all come in through our nightly import from Salesforce — we don't manually enter phone numbers in TaskFlow.

---

### triage-agent

Thanks for those examples — the pattern of longer country codes getting truncated is a really useful clue.

To help us reproduce this: do you know roughly when the phone numbers started appearing incorrectly? For instance, was it after a specific date's import, or have you noticed it building up over time? And has anything changed recently on either side — a TaskFlow update, a change to the Salesforce export/field format, or a new import configuration?

---

### reporter

I think it started maybe two weeks ago? We didn't catch it right away since not every record is affected — it was only when support started making calls that we noticed. I'm not sure about a TaskFlow update, but I know our dev team made some change to how phone numbers are handled around that time, something about supporting international formats better. The Salesforce side hasn't changed as far as I know — the numbers are still correct in our CRM. It seems like the problem is happening during the import into TaskFlow.

---

### triage-agent

Thanks for that context — the timing lining up with a code change to international phone number handling is a strong lead.

Could you share what version of TaskFlow you're currently running (you can usually find this in Settings → About or in the admin dashboard)? And if your dev team can point you to the specific change they made — a release number, a ticket reference, or even a rough description of what was modified — that would help us zero in on exactly where the truncation is happening during the import.

---

### reporter

I checked and we're on TaskFlow v2.3.1. As for the specific change, I'm not really sure of the details — I'm not a developer myself. I just know from our standup that someone on the team updated the phone number normalizer to handle international formats better. I don't have a ticket number or anything like that handy. You might have better luck checking the release notes or changelog on your end for what changed in that area around two weeks ago.

---

### triage-agent

[RESOLVED] {"title":"Phone number country codes truncated during Salesforce import for codes longer than 2 digits","problem":"International phone numbers imported from Salesforce via the nightly sync are having their country codes truncated. Numbers with country codes of 3 or more digits (e.g., Ireland +353, Antigua +1268) are being cut short, while 1-2 digit country codes (US +1, UK +44) are unaffected. Approximately 50-100 of 2,000 customer records are affected. The data is correct in Salesforce, so the corruption occurs during TaskFlow's import processing.","root_cause_hypothesis":"A recent change to the phone number normalizer (approximately 2 weeks ago, intended to improve international format support) likely introduced a bug that truncates country codes to a maximum of 2 digits. This could be a fixed-length substring, an incorrect parsing regex, or a faulty country-code lookup table that silently falls back to a prefix match.","reproduction_steps":["Set up a Salesforce export containing phone numbers with country codes of varying lengths: +1 (US), +44 (UK), +353 (Ireland), +1268 (Antigua)","Run the nightly import process (or trigger it manually) on TaskFlow v2.3.1","Compare the imported phone numbers against the Salesforce source","Observe that +1 and +44 numbers are correct, but +353 is stored as +35 and +1268 is stored as +12 or similar truncation"],"environment":"TaskFlow v2.3.1, nightly import from Salesforce, international phone numbers in E.164 or similar format","severity":"high","impact":"Support team cannot reach affected international customers. ~50-100 customer records have corrupted phone numbers. Silent data corruption — no errors are raised, so the issue compounds with each nightly import. Existing corrupted records will need a backfill from Salesforce.","recommended_fix":"Investigate the phone number normalizer change made approximately 2 weeks ago. Look for logic that parses or validates country codes — likely a fixed-width truncation or an incomplete country code table. The fix should correctly handle variable-length country codes (1-3 digits per ITU E.164). After fixing, run a one-time backfill of affected records from Salesforce to restore the correct numbers.","proposed_test_case":"Unit test the phone number normalizer with country codes of length 1 (+1), 2 (+44), and 3 (+353), as well as numbers where the country code plus area code could be ambiguous (+1268). Assert that the full country code is preserved in each case and the remaining digits are intact.","information_gaps":["Exact commit or PR that changed the phone number normalizer","Whether the import process logs any warnings or debug output for these records","Whether numbers corrupted in earlier imports are being re-corrupted on each subsequent run or only new/changed records are affected"]}

---
