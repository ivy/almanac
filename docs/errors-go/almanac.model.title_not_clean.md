# A title is filled from something other than clean values

## What happened

A title's values include a reference that is not a number, a bool, a slug or a sha, such as a text field or a whole fact.

## Why it matters

A title is shown to a person as it is, outside any quoting, and the forge renders it. Only a literal format and clean values can fill one, so nothing an attacker wrote can appear in it.

## What to do next

Fill the title from clean fields only. Show text in the body instead, through a map that keeps it hostile all the way to the tracker.
