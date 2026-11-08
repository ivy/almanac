# A workflow passes a fact of the wrong type

## What happened

A reference is used where the vocabulary or the effect takes a different type: a predicate's or observer's argument, a key or subject part, an effect's repository, sha or pull request number, a screen's text, or a patch to check. A fact declared in the model with a type other than the one its step produces is reported the same way.

## Why it matters

The vocabulary's functions are typed Go, and the engine passes facts to them as they are. A fact of another type would fail every run that reached the step, after the run had already spent on the steps before it.

## What to do next

Reference the fact or field the part takes, as the message names it, or use a part of the vocabulary that takes this type. Fields are named as the fact's JSON names them.
