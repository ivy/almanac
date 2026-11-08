# broker

The one webhook ingress for every almanac. `almanac-broker` (in `cmd/almanac-broker`) verifies each delivery's signature in constant time, appends it to the deliveries log (`deliveries/`) in PostgreSQL, and answers `202`. It knows nothing about workflows: an almanac reads the log through its own cursor.

## Running it

| Variable | Meaning |
|---|---|
| `ALMANAC_WEBHOOK_SECRET` | The webhook secret set on the GitHub App. Required. |
| `DATABASE_URL` | The PostgreSQL that holds the log. Required. The broker migrates it on start. |
| `ALMANAC_BROKER_LISTEN` | Where to listen, `127.0.0.1:8789` by default. Only a loopback address is accepted, so put an HTTPS ingress in front. |

It serves:

- `POST /webhooks/github`: `202` once a delivery is logged, or was already; `401` unsigned or with a bad signature; `400` with a missing or unsafe event or delivery ID, or a body that is not JSON; `413` over 25 MB; `503` when the log does not take it within 5 seconds, well inside GitHub's 10.
- `GET /healthz`: `200` while the process runs.
- `GET /readyz`: `200` when the database answers, `503` otherwise.

It tells systemd it is ready (`Type=notify`) once it listens, and on `SIGTERM` finishes requests in flight for up to 20 seconds. It logs JSON to standard error, and never logs a payload.

## Recorded deliveries

`almanac-broker replay DIR --url URL` signs each recorded delivery in `DIR` with `ALMANAC_WEBHOOK_SECRET` and posts it to `URL`, such as `http://127.0.0.1:8789/webhooks/github`, as GitHub would. The broker logs a delivery once, so replaying a directory twice is harmless. It prints one line per file with the broker's answer, and exits non-zero if any was refused.

A recorded delivery is one file named `NAME.delivery`: two header lines, a blank line, then the body exactly as GitHub sent it, byte for byte, with nothing added after it.

```text
X-GitHub-Event: pull_request
X-GitHub-Delivery: 72d3162e-cc78-11e3-81ab-4c9367dc0958

{"action":"opened","number":1,...}
```

- Header names are case-insensitive, and lines may end in LF or CRLF. No other header is allowed: the signature is computed when replaying.
- Files are replayed in name order, so name them by their place in the log, such as `000042-72d3162e.delivery`. Other files in the directory are ignored.
- The event and delivery ID must each be 1 to 128 ASCII letters, digits, `-` or `_`, or the broker answers `400`.

To record from a deliveries log, where a row has `seq`, `delivery_id`, `kind` and `body`: the event is the part of `kind` between the first and second dots (`github.pull_request.opened` is `pull_request`), the delivery ID is `delivery_id`, and the body is `body`, written as raw bytes.
