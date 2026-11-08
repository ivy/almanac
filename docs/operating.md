# Stop Almanac, or find out what it did

## Stop it writing

Remove `ALMANAC_MODE=live` from `~/.config/almanac/almanac-renovate.env`, then:

```bash
systemctl --user restart almanac@almanac-renovate
```

It keeps scanning and decides as before, and applies nothing.

## Stop it completely

```bash
systemctl --user stop almanac@almanac-renovate
```

No new run starts, a run in progress gets 30 seconds, and anything unfinished resumes on the next start. To stop it for one repository, remove the App from that repository.

## See where things stand

```bash
almanac-renovate report
```

[Report states](report-states.md) explains the output.

## See what it did

Each finished run is in the journal, with how many effects it produced:

```bash
journalctl --user -u almanac@almanac-renovate --since today
```

A repair is a commit by `almanac[bot]` on the pull request's branch, and its trailer names the run: `Almanac-Key: run-7:0`. A question is a comment on the pull request.

## Check it is healthy

```bash
curl -f http://127.0.0.1:8788/healthz
curl -f http://127.0.0.1:8787/readyz
```

The daemon also scans its repositories on a schedule, every 15 minutes unless configured longer, and logs `scheduled scan finished` each time, with how many pull requests it found and how long the scan took. A gap longer than the interval plus the last scan's duration means scanning has stopped.

## When it reports an error

Every error has a code. `almanac-renovate explain CODE` prints what happened, why it matters, and what to do.
