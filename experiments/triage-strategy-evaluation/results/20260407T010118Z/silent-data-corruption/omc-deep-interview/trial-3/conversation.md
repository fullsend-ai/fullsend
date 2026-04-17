# Conversation: silent-data-corruption x omc-deep-interview

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

Could you share a couple of specific examples of the corrupted phone numbers? For instance, what does a mangled number look like in TaskFlow versus what the correct number should be? (e.g., is it missing the area code, losing trailing digits, dropping the country code, etc.) That will help us narrow down where the corruption is happening.

---

### reporter

Yeah, so for example we have a customer in Ireland whose number should be +353 1 234 5678 but in TaskFlow it shows up as +35 1 234 5678 — the country code got cut short. Another one is a customer in Antigua, their number starts with +1268 but it's showing as +12 something. Meanwhile our US and UK customers seem fine — their numbers with +1 and +44 look correct. So it seems like it's specifically the longer country codes that are getting chopped off, if that makes sense.

---

### triage-agent

That's a really helpful pattern — it does look like country codes longer than two digits are getting truncated. That narrows things down a lot.

Can you tell me how these customer records were entered into TaskFlow? For example, were they imported in bulk (CSV, API sync, migration from another system), or were they added manually through the UI over time? And do you have a sense of whether this has always been the case, or whether the numbers were correct at some point and then became corrupted — say, after a TaskFlow update or a data migration?

---

### reporter

They come in through a nightly batch import from our Salesforce CRM — it syncs automatically. And yeah, I'm pretty sure these numbers were correct before. I think it started maybe two weeks ago? We didn't notice right away because not every record is affected. I'm not sure if there was a TaskFlow update around that time or not, but the numbers were definitely fine before that.

---

### triage-agent

That timeline is really useful, thank you. One key thing that will help us pinpoint exactly where the corruption is happening: can you check whether those same phone numbers (like the Ireland +353 example) are still showing correctly in Salesforce right now? If the source data is intact, we'll know the problem is in TaskFlow's import process and can focus our investigation there. Also, if your team has access to the import job logs or configuration, do you know if anything about the sync setup changed around two weeks ago — a new field mapping, a different API version, or anything like that?

---

### reporter

Yeah, the numbers in Salesforce are definitely still correct — I checked a few of them after our support team flagged the issue. So it's something happening during the import into TaskFlow. As for changes to the sync setup, I'm not really sure to be honest. I don't manage the import configuration myself. I think one of our developers made some update to how phone numbers are handled recently, but I'd have to check with them on the details. I know we didn't change any field mappings or API settings on our end though.

---

### triage-agent

That's very helpful — it's clear the issue is on the TaskFlow import side. One important thing for urgency: is the nightly Salesforce sync still running right now? If so, it may be re-truncating those phone numbers on every import cycle, which means even if someone manually corrected a record in TaskFlow, it would get overwritten the next night. If it is still running, your team may want to consider pausing it until this is fixed to prevent further data corruption.

---

### reporter

Oh good point, yeah the sync is still running every night as far as I know. We haven't turned it off. That's a good call — I'll ask our dev team to pause it until this gets sorted out. We do have backups of the correct numbers in Salesforce so we can restore them once the issue is fixed, but yeah we should probably stop it from overwriting things in the meantime. Thanks for flagging that.

---

### triage-agent

[RESOLVED] {"title":"Phone number country codes truncated to 2 digits during Salesforce nightly import","problem":"Customer phone numbers with country codes longer than 2 digits (e.g., +353 for Ireland, +1268 for Antigua) are being truncated during the nightly Salesforce-to-TaskFlow batch import. Numbers with 1-2 digit country codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of 2,000 customer records are corrupted.","root_cause_hypothesis":"A recent change to TaskFlow's phone number import/parsing logic (made approximately two weeks ago) is truncating the country code portion of international phone numbers to a maximum of 2 digits. The truncation pattern suggests a field length constraint, substring operation, or regex pattern that assumes country codes are at most 2 digits — when in fact they can be 1-3 digits per the E.164 standard.","reproduction_steps":["Set up a Salesforce test record with a phone number using a 3-digit country code (e.g., +353 1 234 5678 for Ireland)","Run the nightly batch import process (or trigger it manually)","Observe the imported phone number in TaskFlow — expect the country code to be truncated to 2 digits (e.g., +35 1 234 5678)","Compare with a record using a 1-2 digit country code (e.g., +1 or +44) to confirm those import correctly"],"environment":"TaskFlow production environment with nightly batch import from Salesforce CRM. Issue began approximately two weeks ago (~late March 2026). The sync is currently still active and re-corrupting data nightly.","severity":"high","impact":"50-100 customer contact records have incorrect phone numbers. Support team is unable to reach affected customers by phone. The nightly sync is actively re-corrupting any manually corrected records. Affected customers are those in countries with 3-digit country codes (e.g., Ireland +353, Antigua +1268, and likely others). US and UK customers are unaffected.","recommended_fix":"1. Review recent changes (last ~2 weeks) to the phone number import/parsing logic in the Salesforce sync pipeline. Look for substring operations, regex patterns, or field length constraints that limit country codes to 2 digits. 2. Fix the parsing to support 1-3 digit country codes per the E.164 standard. 3. After fixing, re-run the import to restore correct numbers from Salesforce (source data confirmed intact). 4. Consider adding validation that flags phone numbers that change during import as a safeguard.","proposed_test_case":"Unit test the phone number import parser with numbers covering all country code lengths: 1-digit (+1 US), 2-digit (+44 UK), and 3-digit (+353 Ireland, +1268 Antigua, +880 Bangladesh). Assert that the full country code is preserved in each case. Additionally, add an integration test that imports a batch containing mixed country code lengths and verifies all are stored correctly.","information_gaps":["Exact code change made ~2 weeks ago to phone number handling (reporter does not manage this; dev team would know)","Whether the issue affects numbers edited/added via the TaskFlow UI or only the batch import path","Complete list of affected country codes beyond the two examples provided"]}

---
