# Conversation: silent-data-corruption x omc-deep-interview

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

Could you share a couple of examples of what the corrupted phone numbers look like compared to what they should be? For instance, if a number should be `+1 (555) 867-5309`, what does TaskFlow currently show instead? Knowing the exact pattern of corruption (are digits missing from the beginning, the end, are formatting characters stripped, etc.) will help us narrow down the cause.

---

### reporter

Yeah, so for example we have a customer in Ireland whose number should be +353 1 234 5678 but in TaskFlow it shows as +35 1 234 5678 — the country code got cut short. We also have a customer from Antigua, their number starts with +1268 but it's showing as +12 something. It seems like the beginning of the number is getting chopped. Oddly enough, our US and UK customers seem fine — their numbers with +1 and +44 look correct. It's the ones with longer country codes that are messed up.

---

### triage-agent

That pattern is really helpful — it looks like country codes longer than two digits are getting truncated to exactly two digits, which would explain why +1 and +44 are fine but +353 and +1268 are not.

To help us track down where the corruption is happening: do you know how these customer phone numbers were entered into TaskFlow? For example, were they imported in bulk (CSV upload, API sync from another system, etc.), entered manually through the UI, or a mix? And did you notice this recently — like after an update or a particular import — or is it possible it's been this way for a while and your support team just now caught it?

---

### reporter

They come in through a nightly batch import — we sync from Salesforce. The records are imported automatically, nobody's typing these in manually. As for timing, I think it's been maybe two weeks? Our support team only noticed last week when they started getting wrong numbers, but looking back at it I think the numbers were already wrong before that. I'm not 100% sure exactly when it started but it definitely wasn't always like this — these numbers were correct before.

---

### triage-agent

That narrows things down a lot — the nightly Salesforce sync is the clear entry point.

Do you know if anything changed around two weeks ago on either side of that sync? For example: a TaskFlow update or migration, a change to the import job configuration or field mappings, or any changes on the Salesforce side (new fields, a schema update, a different phone number format)? Even something that seemed unrelated at the time could be the trigger.

Also, are you able to check what the phone numbers look like in Salesforce right now for those affected records — are they still correct there, or were they already truncated before the import?

---

### reporter

The numbers in Salesforce are still correct — I checked a few of the affected ones and they have the full country codes there. So the problem is definitely happening during the import into TaskFlow, not on the Salesforce side. As for changes, I'm not sure exactly what changed on the TaskFlow side — I think there might have been an update around that time but I don't manage that part. I know our Salesforce setup hasn't changed recently. You'd have to check with your team if something changed in the import process about two weeks ago.

---

### triage-agent

[RESOLVED] {"title":"Salesforce import truncates phone country codes longer than 2 digits","problem":"The nightly batch import from Salesforce is truncating international phone number country codes to exactly 2 digits. Numbers with short country codes (+1, +44) are unaffected, but longer codes like +353 (Ireland) become +35 and +1268 (Antigua) become +12. Approximately 50-100 of 2,000 customer records are affected.","root_cause_hypothesis":"A change approximately two weeks ago in the TaskFlow import pipeline — likely a field mapping, column type change, or parsing regex — is truncating the country code portion of phone numbers to 2 characters. This could be a database column constraint (e.g. VARCHAR(2) on a country_code field), a format-parsing regex that captures only 2 digits after the '+', or a migration that altered phone number storage.","reproduction_steps":["Identify a Salesforce contact record with a country code longer than 2 digits (e.g. +353 for Ireland)","Confirm the full phone number is correct in Salesforce","Run or wait for the nightly batch import to process that record","Check the imported phone number in TaskFlow — the country code should be truncated to 2 digits","Compare against a record with a 1-2 digit country code (e.g. +1 or +44) to confirm those are unaffected"],"environment":"Nightly batch import from Salesforce into TaskFlow. Issue started approximately two weeks ago. Salesforce data remains correct; corruption happens during the TaskFlow import process.","severity":"high","impact":"50-100 customer contact records have corrupted phone numbers, causing the support team to reach wrong numbers. This is an active data integrity issue that worsens with each nightly import if new international records are synced.","recommended_fix":"1. Check git history / deploy logs for changes to the Salesforce import job from ~2 weeks ago. 2. Look for column length constraints on phone or country_code fields (recent migration?). 3. Inspect the phone number parsing/formatting logic for a regex or substring that limits country code to 2 characters. 4. Fix the truncation. 5. Run a one-time remediation to re-import affected records from Salesforce to restore correct numbers.","proposed_test_case":"Import a phone number with a 3-digit country code (e.g. +353 1 234 5678) and a 4-digit code (e.g. +1268 555 1234) through the Salesforce import pipeline. Assert that the full country code is preserved in TaskFlow's database and displayed correctly in the UI.","information_gaps":["Exact TaskFlow change or deployment that coincided with the onset ~2 weeks ago (internal investigation needed)","Whether the phone number field is stored as a single string or split into country_code + number components","Whether records corrupted by past imports have been permanently overwritten or if prior correct values are recoverable from backups"]}

---
