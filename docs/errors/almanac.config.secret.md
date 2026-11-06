# A secret was put in the config file

## What happened

A key in the config file looks like a credential.

## Why it matters

Config files get committed and copied. Secrets come only from the environment, so that a config file is always safe to share.

## What to do next

Remove the key and set the secret in the environment. If the value was ever committed, rotate it.
