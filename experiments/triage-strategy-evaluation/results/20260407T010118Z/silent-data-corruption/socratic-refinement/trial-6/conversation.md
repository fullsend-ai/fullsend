# Conversation: silent-data-corruption x socratic-refinement

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for flagging this — data integrity issues like this are definitely something we want to get to the bottom of.

To start, can you help me understand the bigger picture: how do these customer phone numbers typically get into TaskFlow? Are they entered manually by your team, imported in bulk (like a CSV upload), pulled from another system via an integration, or some mix of those? And do you have a sense of whether these wrong numbers were correct at some point and then changed, or whether they may have been wrong from the moment they were entered?

---

### reporter

They come in through a nightly import from our Salesforce CRM. We have an automated sync that runs every night and updates the customer records in TaskFlow. The numbers were definitely correct before — they're still correct in Salesforce actually. So something is happening during the import or on the TaskFlow side. I think we first started noticing the bad numbers maybe 2 weeks ago, but I'm not 100% sure on the exact timing.

---

### triage-agent

That's really helpful — knowing the numbers are still correct in Salesforce narrows things down a lot. Something in the sync pipeline or on the TaskFlow side is altering them.

I'd love to follow that thread a bit further: can you tell me more about what the mangled numbers actually look like? For example, if a correct number in Salesforce is `+1 (555) 867-5309`, what does the corresponding wrong number look like in TaskFlow — is it missing digits, missing the country code, showing extra characters, or something else? If you could share a couple of before/after examples (redacted if needed), that would be incredibly useful.

Also, you mentioned this started around 2 weeks ago — do you recall anything that changed around that time? A TaskFlow update, a change to the Salesforce sync configuration, new fields added to the import, anything like that?

---

### reporter

So I looked at a few of the bad ones. One customer in Ireland had +353 something something in Salesforce, but in TaskFlow it shows as +35 followed by the rest of the number — like the country code got chopped. Another one I think was a Caribbean number, +1268, and it got cut down to +12. But the weird thing is our US customers with +1 numbers all seem fine, and I checked a couple UK ones with +44 and those look okay too. So it's like... only some country codes are getting messed up?

As for what changed 2 weeks ago, I'm not sure honestly. I don't manage the TaskFlow admin side, our dev team handles that. I vaguely remember someone mentioning they updated something related to phone number formatting to handle international numbers better, but I could be wrong about that. You might want to check the changelog or whatever around that timeframe.

---

### triage-agent

[RESOLVED] {"title":"Nightly Salesforce sync truncates phone country codes longer than 2 digits","problem":"Customer phone numbers imported from Salesforce via the nightly sync are being corrupted. Country codes with 3 or more digits are truncated to 2 digits (e.g., Ireland's +353 becomes +35, Caribbean +1268 becomes +12). Numbers with 1-2 digit country codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of ~2,000 records are impacted.","root_cause_hypothesis":"A recent change (~2 weeks ago) to phone number formatting or international number handling introduced a bug that parses or stores country codes with a fixed 2-digit width, truncating any country code longer than 2 digits.","reproduction_steps":["Find a customer record in Salesforce with a phone number whose country code is 3+ digits (e.g., +353 for Ireland, +1268 for Antigua)","Wait for (or manually trigger) the nightly Salesforce-to-TaskFlow sync","Compare the phone number in TaskFlow to the Salesforce source — the country code should be truncated to 2 digits"],"environment":"TaskFlow with Salesforce CRM integration via nightly automated sync","severity":"high","impact":"Support team cannot reach customers with 3+ digit country codes. Affects ~50-100 records currently, and will corrupt additional records on every future sync run. Outbound calls to affected customers reach wrong numbers.","recommended_fix":"Check the git log / changelog for changes made approximately 2 weeks ago related to phone number formatting or international number handling. Look for any parsing logic that extracts or formats country codes — likely a substring, regex, or field-width issue that caps country codes at 2 digits. Fix the parsing to handle variable-length country codes (1-3 digits per E.164). After fixing, re-sync affected records from Salesforce to restore correct numbers.","proposed_test_case":"Unit test: given phone numbers with country codes of varying lengths (+1, +44, +353, +1268, +86), verify that the sync/formatting pipeline preserves the full number unchanged. Include edge cases for country codes of 1, 2, and 3 digits, and for NANP numbers like +1268 where the full dialing prefix is 4 digits.","information_gaps":["Exact date and content of the code change that introduced the bug (dev team can check their own changelog)","Whether already-corrupted records will be automatically corrected by a re-sync or need a manual data fix","Whether any other fields in the sync are similarly affected by the formatting change"]}

---
