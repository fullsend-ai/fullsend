# Conversation: silent-data-corruption x superpowers-brainstorming

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely worth getting to the bottom of.

To help narrow down the cause, can you tell me when this started? Specifically:

1. **A recent import or migration** — did you recently bulk-import or migrate customer data into TaskFlow?
2. **A recent update** — did this start after a specific TaskFlow update or code change?
3. **Gradual accumulation** — the bad numbers have been trickling in over time as records are created/edited?
4. **Not sure** — you just noticed it now but don't know when it started?

This will help us figure out whether we're looking at a one-time data issue or an ongoing bug.

---

### reporter

I think it's a combination of 2 and 3. We have a nightly batch import that syncs customer data from our Salesforce CRM into TaskFlow. I believe something changed about two weeks ago — that's roughly when our support team started noticing the bad numbers. But it wasn't all at once, it's been more like every day a few more numbers seem to be wrong. So it feels like the nightly sync is gradually messing things up. I'm not 100% sure what changed two weeks ago on the TaskFlow side, but the timing lines up.

---

### triage-agent

That's really helpful — the nightly Salesforce sync is a strong lead.

Can you share what the corrupted numbers actually look like compared to what they should be? For example, if a correct number is `+1 (555) 867-5309`, what does the bad version look like in TaskFlow?

1. **Truncated** — digits are missing from the beginning or end (e.g., `867-5309` or `+1 (555) 867-530`)
2. **Formatting stripped** — the number loses its formatting (e.g., `15558675309`)
3. **Garbled characters** — strange characters appear (e.g., `+1 (555) 8ä7-5309`)
4. **Something else** — happy to hear what you're seeing

Even one or two before/after examples would go a long way toward pinpointing the bug.

---

### reporter

It's truncated, but in a weird way. The country code part seems to get cut short. Like, we have some customers in Ireland and their numbers should start with +353 but in TaskFlow they show up starting with +35. And I think we had one from the Caribbean somewhere that was similarly chopped. The weird thing is our US and UK customers seem fine — their numbers look normal. So it's not all numbers, just some of the international ones. I haven't done a super thorough audit but that's the pattern I've noticed so far.

---

### triage-agent

[RESOLVED] {"title":"Nightly Salesforce sync truncates international phone country codes longer than 2 digits","problem":"The nightly batch import from Salesforce CRM into TaskFlow is truncating country codes on international phone numbers to 2 digits. Numbers with 3-digit country codes (e.g., Ireland +353 → +35) are corrupted, while numbers with 1-2 digit country codes (US +1, UK +44) are unaffected. This has been happening gradually since roughly two weeks ago, with a few more records corrupted each nightly sync. An estimated 50-100 of ~2,000 customer records are affected so far.","root_cause_hypothesis":"A change approximately two weeks ago in the phone number import/parsing logic — likely in country code extraction or a database column constraint — is truncating country codes to a maximum of 2 characters. This could be a regex pattern that assumes country codes are 1-2 digits, a VARCHAR(2) column or field length limit, or a parsing library update that changed behavior.","reproduction_steps":["Identify a Salesforce customer record with a 3+ digit country code (e.g., Ireland +353 xxx xxxx)","Run the nightly sync process (or trigger it manually for that record)","Check the resulting phone number in TaskFlow — the country code should be truncated to 2 digits (+35)"],"environment":"TaskFlow with nightly batch import from Salesforce CRM. Issue began approximately 2 weeks ago, suggesting a recent code or configuration change.","severity":"high","impact":"Support team cannot reach international customers with 3+ digit country codes. ~50-100 records affected and growing daily with each sync. Data integrity issue that erodes trust in the customer database.","recommended_fix":"1. Check git history for changes ~2 weeks ago in the Salesforce sync / phone number import code path. 2. Look for country code field length constraints (DB column, validation regex, parsing logic) that limit to 2 characters. 3. Fix the constraint to allow 3-digit country codes (max is 3 digits per E.164). 4. Re-sync affected records from Salesforce to restore correct numbers.","proposed_test_case":"Import phone numbers with 1-, 2-, and 3-digit country codes (+1, +44, +353, +598) through the sync pipeline and verify all are stored and displayed correctly with full country codes intact.","information_gaps":["Exact mechanism of the Salesforce sync (API, CSV, middleware) — would help locate code but not change the fix","Precise change that occurred two weeks ago (reporter is unsure)","Whether any 3-digit country code numbers are unaffected (would clarify if truncation is universal or conditional)"]}

---
