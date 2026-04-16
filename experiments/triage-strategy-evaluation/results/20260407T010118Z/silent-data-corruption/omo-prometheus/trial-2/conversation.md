# Conversation: silent-data-corruption x omo-prometheus

## Issue

**Some customer phone numbers are wrong in TaskFlow**

We noticed that some of our customer contact records have incorrect phone numbers. The numbers look truncated or mangled. Our support team caught it because they tried calling a few customers and got wrong numbers. We have about 2,000 customer records and I'd estimate maybe 50-100 have wrong numbers. This is a serious data integrity issue for us.

---

## Comments

### triage-agent

Thanks for reporting this — data integrity issues like this are definitely worth investigating urgently.

To start narrowing this down: can you share 2-3 specific examples of the mangled phone numbers alongside what the correct numbers should be? For instance, are digits being dropped from the beginning or end, are certain characters appearing, or are the numbers being reformatted in some way?

Also, do you know roughly when this started? Were these numbers correct at some point in the past, or could they have been wrong since they were first entered/imported?

---

### reporter

Sure, let me pull up a couple examples. One customer in Ireland had +353 1 234 5678 as their number in our CRM, but in TaskFlow it's showing as +35 1 234 5678 — so it's like the country code got chopped. Another one from the Caribbean somewhere, I think Antigua, their number started with +1268 but TaskFlow has it as +12 something. Meanwhile our US and UK customers seem fine, their numbers look correct.

As for timing, I think this started maybe 2 weeks ago? The numbers were definitely correct before that because our support team was using them without issues. We do a nightly sync from Salesforce so I'm guessing something went wrong with one of those imports, but I'm not sure exactly which night it started.

---

### triage-agent

Thanks for those examples — that's very helpful. The pattern is clear: it looks like country codes are being truncated to 2 digits during import. +353 (Ireland, 3 digits) becomes +35, and +1268 (Antigua, 4 digits) becomes +12, while +1 (US) and +44 (UK) are unaffected since they're already 1-2 digits.

This points to something in the nightly Salesforce sync. Do you know if anything changed with that sync process around 2 weeks ago? Specifically:

- Was there a code deployment, configuration change, or update to the sync integration?
- Did the phone number field format or schema change on either the Salesforce or TaskFlow side?
- Is the sync handled by a custom integration, a managed package, or a third-party tool like MuleSoft/Zapier?

Any of these would help us pinpoint exactly what introduced the truncation.

---

### reporter

I'm not 100% sure about the details, but I think one of our developers did push an update around that time — something about improving phone number handling to support international formats better. I don't know the specifics of what they changed. The sync itself is a custom integration we built, it pulls data from Salesforce every night and updates TaskFlow. I don't think anything changed on the Salesforce side, the numbers still look correct over there.

---

### triage-agent

Thanks — that code change to improve international phone number handling is almost certainly where the truncation was introduced. Here's my working theory: the update is capping country codes at 2 digits during the sync, which is why +353 (3 digits) and +1268 (4 digits) get chopped, but +1 and +44 pass through fine.

Two things I need to confirm before I can hand this off to your dev team:

1. **Is this still happening?** Since the sync runs nightly, are records that were previously correct getting re-corrupted each night, or did this only affect records that were synced during a specific window? (An easy way to check: look at a record you know was wrong, correct it manually in TaskFlow, and see if tomorrow's sync truncates it again.)

2. **Can you point us to the specific commit or pull request for that phone number handling change?** Even a rough date or the developer's name would help us find it quickly in the git history.

---

### reporter

Yeah, I'm pretty sure it's still happening. We corrected a couple numbers manually last week and I think they got overwritten again the next day, which is really frustrating. We've been holding off on fixing more until this gets resolved because there's no point if the sync just breaks them again.

As for the specific commit or PR, I wouldn't know how to find that — I'm not really on the development side. I think it was Dave who made the change but I'm not certain. You'd have to check with the dev team on that one. It would have been around 2 weeks ago, maybe a bit more.

---

### triage-agent

[RESOLVED] {"title":"Nightly Salesforce sync truncates international phone country codes to 2 digits","problem":"The nightly Salesforce-to-TaskFlow sync is truncating phone number country codes longer than 2 digits. Country codes like +353 (Ireland, 3 digits) become +35, and +1268 (Antigua, 4 digits) become +12. Numbers with 1-2 digit country codes (+1 US, +44 UK) are unaffected. Approximately 50-100 of ~2,000 customer records are affected. The corruption is ongoing — manually corrected numbers are re-corrupted by the next sync run.","root_cause_hypothesis":"A code change deployed approximately 2-3 weeks ago to 'improve international phone number handling' in the custom Salesforce sync integration is capping country codes at 2 digits during parsing or formatting. The logic likely assumes all country codes are 1-2 digits (true for US/UK) and discards remaining digits, rather than consulting the ITU E.164 variable-length country code table (1-3 digits).","reproduction_steps":["Identify a customer record in Salesforce with a country code longer than 2 digits (e.g., +353 for Ireland)","Ensure the phone number is correct in Salesforce (e.g., +353 1 234 5678)","Wait for the nightly sync to run (or trigger it manually)","Check the same record in TaskFlow — the country code will be truncated to 2 digits (e.g., +35 1 234 5678)"],"environment":"Custom-built Salesforce-to-TaskFlow sync integration, runs nightly. Change was made by a developer likely named Dave, deployed approximately 2-3 weeks ago.","severity":"high","impact":"50-100 customer records have corrupted phone numbers. Support team cannot reach affected customers by phone. Manual corrections are overwritten nightly, so the issue compounds and blocks remediation. Affected customers are those in countries with 3+ digit country codes (much of Africa, Caribbean, parts of Europe and Asia-Pacific).","recommended_fix":"1. Check git history from ~2-3 weeks ago for changes to phone number parsing/formatting in the sync integration (likely authored by Dave). 2. Fix the country code parsing to handle variable-length codes per E.164 (1-3 digits) — consider using a library like libphonenumber rather than hand-rolling parsing. 3. After deploying the fix, run a one-time re-sync or corrective script to restore the truncated numbers from Salesforce (the source data is still correct). 4. Add validation to the sync that flags or rejects phone numbers that change length unexpectedly, as a guardrail against future regressions.","proposed_test_case":"Unit test the phone number parsing function with country codes of varying lengths: +1 (1 digit, US), +44 (2 digits, UK), +353 (3 digits, Ireland), +1268 (4 digits, Antigua). Assert that the full country code and subscriber number are preserved in each case. Additionally, add an integration test that syncs a Salesforce record with a 3-digit country code and verifies the TaskFlow record matches exactly.","information_gaps":["The exact commit or PR that introduced the change (developer team can find this in git history)","Whether phone numbers with other formatting issues exist beyond country code truncation (e.g., extension handling, special characters)","The exact date the corruption started (sync logs or record timestamps should reveal this)"]}

---
