# A push was refused

## What happened

A patch was not pushed, because the branch is not the pull request's own, the pull request is closed or from a fork, or the branch moved after the patch was checked.

## Why it matters

A patch may only reach the branch it was made for, and only on top of the commit it was checked against.

## What to do next

Nothing needs fixing here. A later run will read the branch again and decide afresh.
