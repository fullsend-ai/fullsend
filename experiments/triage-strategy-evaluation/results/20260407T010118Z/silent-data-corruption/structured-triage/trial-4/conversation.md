# Conversation: silent-data-corruption x structured-triage

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

Could you share a couple of examples of what the corrupted phone numbers look like compared to what they should be? For instance, if a number should be `+1 (555) 867-5309`, what does TaskFlow show instead? Knowing the specific pattern of truncation or mangling will help us narrow down where things are going wrong (e.g., during import, save, or display).

---

### reporter

Yeah, so one example — we have a customer in Ireland whose number should be +353 1 234 5678, but in TaskFlow it's showing as +35 1 234 5678. Another one is a customer in the Caribbean, I think Antigua, their country code is +1268 and it got cut down to +12 something. The weird thing is our US and UK customers seem fine. It's mostly international numbers from certain countries that look wrong. It's like the beginning of the number is getting chopped somehow.

---

### triage-agent

Thanks for those examples — that's really helpful. The pattern of country-code truncation on certain international numbers gives us a solid lead.

To help us figure out *where* in the pipeline this is happening: how were these customer phone numbers entered into TaskFlow? For example, were they typed in manually through the UI, imported from a CSV or spreadsheet, synced from another system (like a CRM), or something else? And do you know roughly when the bad numbers first appeared — has this always been an issue, or did it start after a particular update or import?

---

### reporter

They come from our Salesforce CRM — we have a nightly batch import that syncs customer data into TaskFlow. As for when it started, I'm not 100% sure but I think it's been going on for about two weeks now. I didn't notice right away because the numbers looked almost right at a glance. I feel like something might have changed around that time but I'm not sure what exactly. Our support team only caught it last week when they started getting wrong numbers calling international customers.

---

### triage-agent

Thanks for that context — the nightly Salesforce import is a great lead.

One key thing that would help us pinpoint where the corruption is happening: when you look at those same affected records **in Salesforce itself**, are the phone numbers correct there? For example, does the Ireland customer still show `+353 1 234 5678` in Salesforce, but `+35 1 234 5678` in TaskFlow?

This will tell us whether the issue is in how TaskFlow receives and processes the data during import, or whether the numbers are already wrong upstream.

---

### reporter

Yes, the numbers in Salesforce are correct. I double-checked a few of the affected records and they all look fine on the Salesforce side. So it's definitely something happening when TaskFlow pulls them in during the import.

---

### triage-agent

Thanks for confirming the Salesforce data is clean — that definitively narrows it down to the import process on the TaskFlow side.

Could you tell us what version of TaskFlow you're running, and whether any updates or configuration changes were applied around two weeks ago (when the issue likely started)? For example, a TaskFlow version upgrade, a change to the import job settings, or a change to the phone number field mapping in the integration. Any changes around that timeframe — even ones that seemed unrelated — could help us pinpoint what introduced this.

---

### reporter

We're on TaskFlow v2.3.1. I don't remember making any configuration changes ourselves around that time. I vaguely recall seeing something in the changelog or release notes about phone number formatting improvements a couple weeks back, but I'm not 100% sure if that lines up exactly. We didn't change anything on our end with the import job settings or field mappings — those have been the same for months.

---

### triage-agent

[RESOLVED] {"title":"International phone numbers with 3+ digit country codes are truncated during Salesforce batch import","problem":"The nightly Salesforce-to-TaskFlow batch import is truncating international phone numbers for countries with longer country codes. For example, Ireland's +353 becomes +35 and Antigua's +1268 is similarly cut. US (+1) and UK (+44) numbers are unaffected. Approximately 50-100 of ~2,000 customer records are impacted.","root_cause_hypothesis":"A recent change (likely in TaskFlow v2.3.1, possibly the 'phone number formatting improvements' the reporter vaguely recalls from the changelog) introduced a bug in phone number parsing or normalization during import. The code likely assumes a maximum country code length of 2 digits, or applies a fixed-width truncation to the leading portion of the number. This would explain why +1 (US) and +44 (UK) are fine but +353 (3 digits) and +1268 (4 digits as a sub-code) are corrupted.","reproduction_steps":["Set up a Salesforce integration with TaskFlow v2.3.1 containing customer records with international phone numbers (3+ digit country codes, e.g., +353 for Ireland, +1268 for Antigua)","Run the nightly batch import (or trigger it manually)","Compare the imported phone numbers in TaskFlow against the Salesforce originals","Observe that country codes longer than 2 digits are truncated"],"environment":"TaskFlow v2.3.1, Salesforce CRM integration via nightly batch import, standard field mappings (unchanged for months)","severity":"high","impact":"~50-100 customer records have incorrect phone numbers, causing the support team to reach wrong numbers when contacting international customers. This is a data integrity issue that erodes trust in the system and directly impacts customer communication.","recommended_fix":"1. Check the v2.3.1 changelog for any phone number formatting or normalization changes. 2. Inspect the Salesforce import pipeline's phone number parsing/normalization code — look for assumptions about country code length (e.g., fixed 2-digit extraction or truncation). 3. Fix the parsing to handle variable-length country codes per the E.164 standard. 4. Provide a one-time re-import or correction script to fix the ~50-100 affected records from the clean Salesforce data.","proposed_test_case":"Unit test the phone number normalization function with international numbers of varying country code lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4-digit NANP sub-code). Assert that all are stored without truncation. Additionally, integration-test a batch import with a mix of these numbers and verify round-trip fidelity.","information_gaps":["Exact TaskFlow version or commit that introduced the regression (reporter unsure if the changelog note about 'phone number formatting improvements' aligns with the timeline)","Import job logs that might show warnings or errors during phone number processing","Whether previously-correct records were retroactively corrupted by re-import or only newly created records are affected"]}

---
