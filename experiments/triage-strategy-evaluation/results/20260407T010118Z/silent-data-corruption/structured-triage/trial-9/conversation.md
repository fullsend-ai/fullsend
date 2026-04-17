# Conversation: silent-data-corruption x structured-triage

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

To start narrowing this down: **how are these customer phone numbers getting into TaskFlow?** For example, are they entered manually through the UI, imported via CSV/bulk upload, synced from another system, or entered through an API? And when you say the numbers look "truncated or mangled," could you share a couple of examples of what a correct number should look like versus what's actually stored? (Feel free to redact digits for privacy, e.g. `+1 (555) 123-4567` became `555123456`.)

---

### reporter

They come in through a nightly sync from our Salesforce CRM. It's been running for months without issues until recently.

As for examples — I don't have the exact numbers in front of me right now, but from what our support team said, it seems like the country code part gets cut short. Like a number that should start with +353 (Ireland I think?) ends up as just +35 and then the rest of the number. US and UK numbers seem fine though, it's mostly international ones from certain countries. I'm not 100% sure on the exact pattern but that's what it looked like when I glanced at it.

---

### triage-agent

Thanks for those details — the Salesforce sync and the international country code pattern are really helpful clues.

To help us narrow down what changed: **do you know roughly when this started happening?** For example, was there a recent TaskFlow update, a change to the Salesforce sync configuration, or a change on the Salesforce side (new fields, updated phone formats, etc.)? Even a rough timeframe like "we first noticed it last week" or "it showed up after our March update" would help us pinpoint what introduced the issue.

---

### reporter

I think it started about two weeks ago, maybe a bit more. Our support team only caught it a few days ago but when we looked back at the records it seems like it's been happening for a couple weeks. I'm not sure exactly what changed — I vaguely remember getting a notification about an update to the import process or something related to phone number formatting, but I didn't pay much attention to it at the time. Nothing changed on our Salesforce side as far as I know.

---

### triage-agent

Thanks, the timeline is really helpful — two weeks ago lines up well with a potential update being the trigger.

You mentioned getting a notification about an update to the import process or phone number formatting. Could you check what **version of TaskFlow** you're currently running? You can usually find this under Settings → About or in the admin dashboard. If you can also find that notification (maybe in email or in-app notifications), the specific update name or version number would help us pinpoint the exact code change that may have introduced this.

---

### reporter

We're on TaskFlow v2.3.1. I tried looking for that notification but I can't find it now — it might have been a changelog entry or a release note email, I'm not really sure. I just remember seeing something about phone number handling or international format support around that time. Sorry I can't be more specific about it.

---

### triage-agent

[RESOLVED] {"title":"International phone numbers truncated during Salesforce sync since v2.3.1 update","problem":"Customer phone numbers with international country codes (notably 3-digit codes like Ireland's +353) are being truncated during the nightly Salesforce CRM sync. Approximately 50-100 of ~2,000 customer records are affected. US (+1) and UK (+44) numbers appear unaffected, suggesting the issue is specific to certain country code lengths or formats.","root_cause_hypothesis":"A change introduced in or around the TaskFlow v2.3.1 release — likely related to phone number formatting or international format support — is truncating country codes during the Salesforce import/sync pipeline. The most probable cause is a parsing or validation function that assumes a maximum country code length of 2 digits, clipping 3-digit codes like +353 to +35. Alternatively, a field length constraint or regex pattern may have been tightened incorrectly.","reproduction_steps":["Set up a Salesforce sync connection with customer records containing international phone numbers with 3-digit country codes (e.g., +353 for Ireland, +370 for Lithuania)","Include control records with 1-digit (+1 US) and 2-digit (+44 UK) country codes","Run the nightly sync (or trigger a manual sync if supported)","Compare the imported phone numbers in TaskFlow against the Salesforce source records","Verify that 3-digit country codes are truncated while 1- and 2-digit codes are preserved correctly"],"environment":"TaskFlow v2.3.1, Salesforce CRM integration via nightly sync","severity":"high","impact":"Approximately 50-100 customer records have corrupted phone numbers, preventing the support team from reaching those customers. Ongoing nightly syncs continue to introduce or perpetuate the corruption. Affected customers appear to be those with international numbers from countries with 3-digit country codes.","recommended_fix":"1. Check the v2.3.1 changelog/diff for changes to phone number parsing, formatting, or international format handling in the Salesforce sync/import code path. 2. Inspect the phone number normalization or validation function for assumptions about country code length (likely a substring, regex, or field-length issue). 3. Fix the parsing to handle all valid country code lengths (1-3 digits per ITU E.164). 4. Run a remediation script to re-sync or repair the ~50-100 affected records from Salesforce.","proposed_test_case":"Unit test: pass phone numbers with 1-digit (+1), 2-digit (+44), and 3-digit (+353, +370) country codes through the sync/import phone normalization function and assert all country codes are preserved in full. Integration test: run a Salesforce sync with test records covering all country code lengths and verify round-trip fidelity.","information_gaps":["Exact examples of truncated vs. expected phone numbers (reporter did not have them on hand)","Sync job logs or error output that might show warnings during import","The specific v2.3.1 changelog entry the reporter vaguely recalls seeing","Whether the issue affects all 3-digit country codes or only specific ones"]}

---
