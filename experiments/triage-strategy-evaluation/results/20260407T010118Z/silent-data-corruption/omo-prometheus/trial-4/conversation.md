# Conversation: silent-data-corruption x omo-prometheus

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are serious and we want to get to the bottom of it.

To start narrowing this down: can you share 2-3 specific examples of corrupted phone numbers alongside what the correct number should be? For instance, "Record shows 555-123-45 but should be 555-123-4567" or "Shows +1555123 but should be +15551234567." The exact pattern of how the numbers are wrong will tell us a lot about where the corruption is happening (input, storage, or display).

---

### reporter

Sure, let me pull up a few. One customer in Ireland had their number show as +35 812 3456 but it should be +353 1 812 3456. Another one, I think they're in Antigua or somewhere in the Caribbean, their number shows as +12 685551234 but the country code should be +1268. And there's one more from India showing +91... actually wait, that one looks fine. It seems like it's mostly international numbers but not all of them. US and UK numbers look correct from what I can tell.

---

### triage-agent

Thanks for those examples — they're really helpful. I can see a pattern forming around how international country codes are being parsed.

Critical question: **when did this start?** Specifically:
- Did these numbers used to be correct in TaskFlow and became corrupted at some point, or were they entered/imported already wrong?
- Has there been a recent code deploy, data migration, CSV import, or phone number library update in the last few weeks?
- Are the affected records ones that were recently created/edited, or do they span a range of creation dates?

This will help us determine whether we're looking at a regression in the normalization logic vs. a long-standing parsing bug that only manifests with certain country codes.

---

### reporter

These numbers were definitely correct before — our support team has been calling these customers for months with no issues. It started maybe 2 weeks ago? I'm not 100% sure on the exact date but it was around then that we first started getting reports of wrong numbers. We have a nightly batch import that syncs customer data from our Salesforce CRM into TaskFlow, so my guess is something changed in how that import handles the numbers. I don't know if there was a code update or anything on your end around that time, but the timing lines up. The affected records span different creation dates — some are old customers, some newer — but they all seem to have gone bad recently.

---

### triage-agent

Thanks — this is shaping up clearly. The pattern looks like country codes are being truncated during parsing (Ireland's +353 → +35, Antigua's +1268 → +12), which points to a normalization bug that's cutting codes to a fixed length or misidentifying code boundaries.

To confirm this theory, can you check two things for me?

1. **Are the numbers correct in Salesforce right now?** If you look at that Irish customer's record in Salesforce, does it still show the correct +353 1 812 3456?
2. **When you look at the bad number in TaskFlow, where are you seeing it?** Specifically, if you go to the database or API response directly (not just the UI), is the stored value itself truncated, or does it display incorrectly but store correctly?

This will tell us definitively whether the corruption is happening during the import into TaskFlow vs. a recent display/formatting change.

---

### reporter

Just checked Salesforce — yes, the Irish customer's number is correct there, still shows +353 1 812 3456. So the source data is fine, it's definitely getting mangled on the way into TaskFlow.

As for the second question, I'm not really sure how to check the database directly — I'm not a developer. But I can tell you the wrong number shows up everywhere in TaskFlow, on the customer detail page, in exports, etc. So I'd guess it's actually stored wrong, not just a display thing. If it helps, I think the numbers were correct in TaskFlow before the nightly sync overwrote them — like if a record didn't get synced for some reason, it would probably still have the right number. But I haven't verified that specifically.

---

### triage-agent

[RESOLVED] {"title":"Nightly Salesforce import truncates international phone country codes (3+ digit codes)","problem":"The nightly batch import that syncs customer data from Salesforce into TaskFlow is corrupting international phone numbers by truncating their country codes. Numbers with longer country codes (3-4 digits) are affected while shorter codes (1-2 digits like US +1 and UK +44) appear unaffected. Approximately 50-100 of ~2,000 customer records are impacted, and the corruption is persisted to storage — wrong numbers appear in the UI, exports, and presumably the database.","root_cause_hypothesis":"The phone number parsing/normalization logic in the Salesforce-to-TaskFlow import pipeline is truncating country codes to a fixed length (likely 2 digits) or misidentifying country code boundaries. Ireland's +353 becomes +35, Antigua's +1268 becomes +12. This is consistent with a parser that assumes all country codes are 1-2 digits, or a recent change to a phone number parsing library that introduced this behavior. A code deploy or dependency update ~2 weeks ago likely introduced the regression.","reproduction_steps":["Identify a customer record in Salesforce with a 3+ digit country code (e.g., Ireland +353, India +91, Antigua +1268)","Ensure the phone number is correctly formatted in Salesforce","Wait for or manually trigger the nightly batch import sync","Check the resulting phone number in TaskFlow — expect the country code to be truncated"],"environment":"Production. Nightly batch import from Salesforce CRM to TaskFlow. Regression appeared approximately 2 weeks ago (late March 2026). Affects international numbers with 3+ digit country codes; US (+1) and UK (+44) numbers unaffected.","severity":"high","impact":"Support team cannot reach ~50-100 international customers by phone. Data integrity of customer contact records is compromised. Each nightly sync overwrites previously correct numbers with corrupted ones, so the problem compounds as more records are re-synced.","recommended_fix":"1. Check deploy/change history for the import pipeline from ~2 weeks ago — look for changes to phone number normalization, parsing libraries (e.g., libphonenumber), or the Salesforce API integration. 2. Inspect the import code's country code parsing logic for assumptions about country code length. 3. Fix the parser to correctly handle variable-length country codes (1-3 digits per ITU-T E.164). 4. Run a one-time remediation sync to re-import corrected numbers from Salesforce for all affected records. 5. Consider adding a validation check that flags phone numbers whose parsed country code doesn't match a known valid code.","proposed_test_case":"Create test cases for the phone number import/normalization function using numbers with 1-digit (+1 US), 2-digit (+44 UK), and 3-digit (+353 Ireland, +268 Antigua landline) country codes. Assert that the full country code and subscriber number are preserved after normalization. Include edge cases like +1268 (NANP overlay with Antigua) to verify correct code boundary detection.","information_gaps":["Exact date of the first corrupted sync (engineering can check import logs)","Whether a specific code deploy or dependency update coincides with the timeline","Whether records that were not re-synced in the last 2 weeks still have correct numbers","The specific phone number library or normalization function used in the import pipeline"]}

---
