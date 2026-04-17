# Conversation: silent-data-corruption x omc-deep-interview

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely something we want to get to the bottom of quickly.

Could you share a few examples of what the corrupted phone numbers look like compared to what they should be? For instance, if a number should be `+1 (555) 867-5309`, what does TaskFlow currently show instead? Even two or three before/after examples would help us identify the pattern of corruption.

---

### reporter

Sure, let me pull up a couple I remember. We have a customer in Ireland whose number should be +353 1 234 5678 but TaskFlow shows it as +35 1 234 5678 — so it's like the country code got cut short. Another one is a customer in Antigua, their number starts with +1268 but it's showing as +12 and then the rest of the number. Meanwhile our US customers with +1 numbers all seem fine, and I think our UK contacts (+44) look correct too. It seems like it's mainly international numbers from certain countries that are affected, if that helps.

---

### triage-agent

That's really helpful — the pattern is clear: country codes longer than two digits are getting truncated. That narrows things down a lot.

One key thing I'd like to understand: do you know roughly *when* these numbers went wrong? For example, were these customer records imported or migrated from another system at some point, or were they entered directly in TaskFlow? And if someone edits one of these records and re-enters the correct number today, does it stay correct or does it get corrupted again on save?

---

### reporter

Good question. I think they started going wrong maybe two weeks ago? We do a nightly import from our Salesforce CRM that syncs customer data into TaskFlow, so the records aren't entered manually — they come in through that batch process. I haven't tried manually correcting one to see if it sticks, but I'd guess it would just get overwritten the next night when the sync runs again. I'm not 100% sure when it started exactly, but I know those numbers were correct before because our support team was using them without issues until recently.

---

### triage-agent

That timeline is really useful — two weeks ago gives us something concrete to investigate.

Do you recall whether anything changed around that time? For example, did TaskFlow get updated, or was the Salesforce sync configuration modified (new field mappings, a different integration plugin, etc.)? Even something like a Salesforce admin changing the phone number format on their end could be relevant. Any change that coincided with when the numbers started appearing truncated would help us pinpoint the root cause.

---

### reporter

I'm not sure about Salesforce changes — I don't think anything changed on that side, but I'd have to check with our Salesforce admin. On the TaskFlow side, I think there was an update or something around that time, but I don't manage that myself so I'm not 100% certain. I do know the numbers in Salesforce are still correct because our team has been looking them up directly in the CRM when they need to call those customers. So the issue seems to be happening during the import into TaskFlow, not on the Salesforce end.

---

### triage-agent

[RESOLVED] {"title":"Salesforce import truncates international phone country codes longer than 2 digits","problem":"The nightly Salesforce-to-TaskFlow customer sync is truncating phone number country codes that are longer than two digits. For example, Ireland's +353 becomes +35, and Antigua's +1268 becomes +12. US (+1) and UK (+44) numbers are unaffected because their country codes are 1-2 digits. Approximately 50-100 of ~2,000 customer records are affected.","root_cause_hypothesis":"The phone number import/parsing logic is truncating or validating country codes against a maximum length of 2 digits. This is likely a regression introduced in a TaskFlow update approximately two weeks ago — possibly a new phone number normalization step, a schema change limiting the country code field width, or a regex/parsing change in the import pipeline.","reproduction_steps":["Set up a Salesforce sync with test customer records containing phone numbers with 1-digit (+1), 2-digit (+44), and 3+ digit (+353, +1268) country codes","Run the nightly import process (or trigger it manually)","Compare the imported phone numbers in TaskFlow against the Salesforce source records","Observe that country codes with 3+ digits are truncated to 2 digits"],"environment":"TaskFlow with Salesforce CRM integration via nightly batch import. Issue began approximately two weeks ago (late March 2026). Salesforce-side data is confirmed correct.","severity":"high","impact":"Support team cannot reach ~50-100 international customers by phone using TaskFlow contact data. Currently working around the issue by looking up numbers directly in Salesforce, which slows down operations. Data corruption recurs nightly since the import overwrites any manual corrections.","recommended_fix":"1. Review git history of the phone number import/parsing code for changes made ~2 weeks ago. 2. Check for country code validation, regex patterns, or database column constraints that limit country code length to 2 characters. 3. Fix the parsing logic to support variable-length country codes (1-3 digits per E.164). 4. Run a one-time re-import or corrective sync to fix the ~50-100 affected records.","proposed_test_case":"Unit test the phone number import parser with E.164 numbers spanning all country code lengths: +1 (1 digit), +44 (2 digits), +353 (3 digits), +1268 (4 digits including trunk prefix). Assert that the full country code and subscriber number are preserved exactly as provided in the source data.","information_gaps":["Exact TaskFlow version or commit that introduced the regression (~2 weeks ago)","Whether the phone number field schema was changed (e.g., column width, type constraint)","Whether any Salesforce-side format changes coincided with the issue"]}

---
