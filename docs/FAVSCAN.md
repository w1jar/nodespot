# Favorites activity scan

Status: implemented in 1.1.10 (package `internal/favstats`, API in `docs/API.md` under "Favorites activity scan", UI in `web/src/components/Favorites.svelte`). The text below is the original design; where the implementation differs, "As built" says so.

## As built (1.1.10)

- The on/off switch is a "Scan" checkbox (a toggle button since beta.11) in the Favorites header on the dashboard (operators and admins, like the other favorites controls; viewers see "Scan on" and the values). Setting `favstats.enabled`, default off, `PUT /api/favorites/scan`. Its tooltip says "Show activity for favorite nodes from stats.allstarlink.org". The header also shows "updated Ns ago", or "rate limited, retrying" during a backoff.
- The rate is not a UI setting: `[favstats] rate` in `nodespot.conf` (default 20 requests per minute, 1..25; 10 in beta.11 and beta.12, 20 again since beta.13), and `[favstats] url` overrides the base URL.
- Activity is a separate snapshot (`GET /api/favorites/stats`) plus the SSE event `favstats`; the favorites objects are unchanged. Nothing is exposed on the public page.
- Token bucket: capacity 4, refill at the configured rate, requests at least 2 s apart. Backoff doubles from 15 s to 5 min, honors Retry-After, and ends on the next success. A 404 for one node drops that node's values without backing off.
- Keyed state uses the API's own `keyed` flag only for a node's first sample; afterwards it is derived from counter changes between distinct reports (`stats.data.time`, which the service refreshes about every 30 s). A poll that returns the same report again repeats the previous keyed state and changes no counters. The elapsed time is the difference of the two report times (poll time when a report time is missing). An implausible delta is a TX time increase larger than the elapsed time plus 2 s. A favorite linked to a local node shows keyed from the local link state immediately (the scan lags by up to 30 s). Keyed is cleared once the node's data is older than 15 minutes (like Active).
- Only `apprptuptime`, `totaltxtime`, `totalkeyups`, `keyed`, `links` (counted) and the top-level `node` object's `Status` / `access_webtransceiver` are decoded; `linkedNodes` is never read. The decoder tolerates numbers as well as strings, and a `node` that is not an object.
- The Node cell is highlighted when the favorite is linked to any local node (the status store data), and the # cell carries the activity color (red keyed, green active, brighter green with WebTransceiver). With the scan on, Description and Location hide below lg / xl widths.
- Demo mode (`--demo`) uses synthetic samples with no rate limit (`favstats.DemoFetcher`).

### Answers to the open questions

- Bulk endpoint: not checked against the live service. The development environment's network policy blocked `stats.allstarlink.org` (connection refused by the egress proxy), so nothing was fetched and no real response data was seen or committed. The one-node-per-request round robin stays. Check on a real node: `curl -s https://stats.allstarlink.org/api/stats/<node>` for a bulk or multi-node endpoint.
- Top-level `node` object: confirmed against a live node (2026-11). The top-level keys are `stats`, `node`, `keyups`, `time`; `node` is an object whose `Status` was "Active" and `access_webtransceiver` was 1, matching what the decoder reads. The decoder reads `node.Status` and `node.access_webtransceiver` only when the top-level `node` is an object, and otherwise reports not active and no WebTransceiver (the Rx%, LCnt and keyed values do not depend on it). Verify on the node that # cells turn green for active favorites.

## Rate limit lessons (1.1.11)

On real hardware the stats service answered 429 after about 10 requests at 20 per minute, so its effective limit is stricter than the documented 30 per minute (or it counts other traffic from the same address). beta.11 changes:

- Valid range is 2000..999999. EchoLink favorites (3000000..3999999) are never queried and show no values.
- Defaults: 10 requests per minute (`[favstats] rate`, 1..25, now a ceiling). (Burst 2 and the token bucket were replaced by even pacing in beta.13, see below.)
- Adaptive rate: each 429 halves the rate in use (floor 3 per minute, or the configured rate if lower) on top of the 15 s to 5 min backoff, which honors Retry-After. After every 20 consecutive successful requests the rate rises by 1 per minute, up to the configured rate. The API reports it as `effRate`, and `retryAt` when the backoff ends.
- A 429 is not a sample: existing values and the "updated" time are kept.
- Polling order: nodes never queried first, then the one queried longest ago, so a backoff cannot starve the end of the list.
- `[favstats] log = true` (default false) writes one journal line per request: `favstats: GET node=<n> status=<code or error> took=<ms> rate=<n>/min gap=<s> retry-after=<s or ->`, plus `favstats: rate lowered to N/min after 429` and `favstats: rate raised to N/min`. Never the response body, other headers or linkedNodes data. To find the real limit, turn it on and watch for the request count before the first 429.
- UI: the header checkbox is replaced by a "Start scan" / "Scanning" toggle button (viewers see a "Scan on" / "Scan off" badge), and during a backoff the status reads "rate limited, retrying in Ns".

## Original design (2026-11-15)

## How AllScan does it

Sources:
- https://raw.githubusercontent.com/davidgsd/AllScan/main/README.md
- https://raw.githubusercontent.com/davidgsd/AllScan/main/js/main.js
- https://raw.githubusercontent.com/davidgsd/AllScan/main/stats/stats.php

UI:
- The favorites table gets two columns, **Rx%** (remote TxTime / Uptime) and **LCnt** (number of connected links). Both are sortable; the first click sorts descending.
- The # cell is color-coded: red while keyed (brighter with recent TX), dark green when Active, slightly brighter green when Active with WebTransceiver.
- The Node cell is highlighted when it is linked to our node.

Data:
- Data comes from `GET /api/stats/<node>` on stats.allstarlink.org, one node per request, proxied by AllScan's server.
- Nodes outside 2000..999999 are skipped (EchoLink nodes are 3000000..3999999, 3 + the EchoLink id).
- Rx% = `round(100*totaltxtime/apprptuptime)` only if uptime > 60 and txtime > 10, otherwise `"-"`.
- `keyed` is unreliable per the AllScan README, so keyed state is derived from changes in `totalkeyups` / `totaltxtime` between polls:
  - `txp = min(100, round(100*Δtx/Δt))`
  - `txAvg = keyed ? 100 : round(txAvg/2 + txp/2)` (moving average)
  - Counter resets and implausible deltas count as 0.

Rate limit:
- The ASL stats API allows 30 requests per minute per IP.
- AllScan round-robins one request at a time: fast after page load (1 s), slowing over time to 2, 3, 4, 6 and 10 s. Never more than 30 per minute.
- On HTTP 429 it waits 15 s.

## API response (verified against a live node (2026-11))

`curl -s https://stats.allstarlink.org/api/stats/<node>` returns JSON. Observed (anonymized, truncated):

```json
{"stats":{"id":12345,"node":50001,"data":{
  "apprptuptime":"1000000","totalexecdcommands":"100","totalkeyups":"1000",
  "totaltxtime":"100000","apprptvers":"hamvoip-1.x","timeouts":"0",
  "links":["50002","50003","50004"],"keyed":false,"time":"1700000000","seqno":"1000",
  "totalkerchunks":"0","nodes":"T50002,T50003,T50004","keytime":"1000010",
  "linkedNodes":[{"name":50002,"Status":"Active","callsign":"N0CALL","access_webtransceiver":"0","server":{"Location":"..."}},{"name":"3355800"}]
}}, ...}
```

Notes:
- Counters are JSON strings, not numbers. `keyed` is a boolean.
- `links` is an array of node-number strings; LCnt = `len(links)`.
- `nodes` has a mode prefix per link (`T` = transceive).
- `linkedNodes` entries carry full registration records for linked nodes, including other operators' IP addresses and callsigns. Parse only the fields we need into dedicated structs. Never store, log or forward the raw response.
- Some `linkedNodes` entries are just `{"name": ...}` (e.g. EchoLink or unregistered nodes).
- The output was cut at 4000 bytes, so the top-level `node` object (with the queried node's own `Status` and `access_webtransceiver`, which AllScan reads as `node.Status`) was not seen here. It was confirmed against a live node (2026-11) (see the answers below).
- The full response is large (tens of KB for busy hubs). Use a generous size cap (e.g. 512 KB) and a streaming decode.

## Proposed design for nodespot (agreed in principle, details open)

Server:
- New package `internal/favstats`: one server-side poller (not per browser), its own `http.Client` (HTTPS, timeout about 5 s, size cap), in-memory `map[node]Stat`, no SQLite table.
- Polls only while at least one logged-in SSE client is connected. Hook the SSE hub's client count, as the monitor's fast/slow switch does. Keeps values for about 10 min after the last viewer leaves.
- Round-robin over distinct valid favorite nodes, skipping our own local nodes.
- Token bucket at 20 requests per minute (setting, max 25). Initial burst about 1 request per 2 s. (Superseded in beta.11, see "Rate limit lessons".)
- Exponential backoff on 429 and errors (15 s to 5 min).

API:
- Optional `activity` on favorites: `{rxPct, linkCnt, active, webTx, keyed, lastSeen}`.
- New SSE event `favstats` (changes only).
- `GET /api/favorites/stats` snapshot.
- Never exposed on the public page.

Settings:
- "Favorites activity scan" on/off. Default off, because it is new outbound traffic. Help text says the node's IP queries stats.allstarlink.org for each favorite node number.
- Rate setting.
- Base URL override in `nodespot.conf` under `[favstats]`.

UI (`web/src/components/Favorites.svelte`):
- Rx% and LCnt columns after Location, both sortable.
- # cell colors as AllScan, reusing the existing `row-tx` / `row-rx` tokens. Keep the contrast fix: white text on red and green.
- Rx% tooltip: "Lifetime TX time as % of uptime".
- Header shows scan state and "updated Ns ago".
- Columns hidden when the scan is off.
- At narrow widths, hide Description and Location first.

Demo mode (`--demo`) returns synthetic stats.

Tests:
- httptest fixtures from the anonymized sample (string counters, boolean `keyed`, short `linkedNodes` entries).
- Rx% edge cases.
- Keyed-delta logic and counter resets.
- Token bucket and 429 backoff with a fake clock.
- Poller start/stop driven by viewer count.
- EchoLink skip.
- Setting off means no requests.
- Endpoint requires login.

Size: about 500 lines of Go including tests, and about 200 lines of Svelte/CSS.

## Open questions (original)

- Check whether ASL now offers a bulk or multi-node stats endpoint (the AllScan README says one was planned). It would replace the round-robin.
- Top-level `node` object: confirmed against a live node (2026-11) (keys `stats`, `node`, `keyups`, `time`; `Status` "Active", `access_webtransceiver` 1).

## Error handling and "Active" (1.1.12)

- Decoding is tolerant: `apprptuptime`, `totaltxtime`, `totalkeyups`, `keyed`, `node.Status` and `node.access_webtransceiver` accept a string, number, bool or null (a wrong type leaves that field zero); `links` is counted from an array of strings or numbers (missing or null is fine); `node` may be an object, null, an array or missing. Everything else (`linkedNodes`, top-level `keyups` and `time`) is skipped token by token, never buffered or logged.
- Size cap 4 MB. A body over the cap fails with "response too large", not a decode error; a `Content-Length` over the cap is refused without reading.
- Global backoff (15 s to 5 min, adaptive rate on 429): network errors and timeouts, HTTP 429, HTTP 5xx.
- Per-node skip (10 minutes, node goes to the back of the queue, no global pause, success streak unchanged): invalid JSON, too-large response, other HTTP 4xx. A 404 also clears that node's values. Journal lines: `favstats: node <n>: response too large (>4 MB), skipping for 10m0s`, `favstats: node <n>: invalid response (<encoding/json message, max 120 chars>), skipping for 10m0s`, `favstats: node <n>: HTTP 404, skipping for 10m0s`, `favstats: request failed (timeout), pausing 15s`.
- Offline or unknown nodes: the stats service answers 200 with `"stats":null` (plus the registration record in `node`), or possibly an empty body, `null`, `{}`, an error object or an HTML page. All of these mean "no data": not an error, no log line, values cleared, asked again after 10 minutes. Only genuinely malformed JSON is logged as invalid.
- `node.Status` is the registration account status ("Active" even for an offline node), so a node is shown Active (and WebTransceiver) only when `stats.data` is present and, if `stats.data.time` (unix seconds) is present, within 15 minutes of now.
- With `[favstats] log = true` the request line shows `status=200`, an HTTP code, `200-nodata`, `decode-error`, `too-large`, `timeout`, `connection error` or `error`.

## Even pacing and progress (1.1.13)

- Pacing: no burst. One request goes out exactly every 60 / effRate seconds (default 20 per minute: every 3 s; 10 per minute: 6 s; from the very first request), never closer than 3 s (so the 25 per minute ceiling really means 20). The schedule is kept from the planned time, not from when the previous answer arrived, so the tick (250 ms) does not make it drift. Backoff after a 429 or error, `Retry-After` and the adaptive rate (halve on 429 down to the floor, +1 per 20 successes) work as before; after a backoff the schedule restarts from the first request.
- Progress in the payload (`GET /api/favorites/stats`, SSE `favstats`): `checking` (node being requested now, `""` when idle), `nextAt` (when the next request is due, or when the backoff ends if later; `null` if the scan is off, nobody is watching or nothing is eligible), `total` (eligible favorites: valid node number 2000..999999, not one of this machine's nodes, counted once) and `checked`. `checked` counts the eligible nodes that have an answer in the current pass: tried since the pass began, or in their 10-minute skip period after an offline or unusable answer. A pass ends when every node that is not in its skip period has been tried; the next request then starts a new pass and `checked` restarts (so it shows `30 of 30` until that request begins).
- `checkedAt` is a separate map `{node: RFC3339}` with the last attempt per node, including offline and no-data results, which have no `stats` entry. A 429 is not an attempt and leaves it alone. Events carry only the changed nodes; snapshots carry all of them.
- `next` (since beta.14): the node that will be requested next, computed by the poller's own ordering (never checked first in favorites order, then the one checked longest ago; nodes in their skip period, your own nodes and out-of-range numbers excluded; the node being requested counts as just checked, so the mark moves on when a request starts). `""` when the scan is off, nobody is watching, nothing is eligible or every node is skipped. It is sent with the request start and end events and, via `Service.Refresh`, when the favorites change. The `checking` field stays for compatibility; the UI ignores it.
- Events: one when a request starts (`checking` set, `nextAt`, progress) and one when it ends (`checking` cleared, `checkedAt` and progress updated, `stats` only if values changed). Both are small; the old 15 s heartbeat is gone because every request now produces events. `updated` stays for compatibility but the UI no longer uses it (it only moved on successful answers, so it climbed misleadingly and never moved for offline nodes).
- UI: the header shows `checked 12 of 30 · next in 4s` (countdown from `nextAt`), `rate limited, retrying in Ns` during a backoff and `no favorites to check` when nothing is eligible. The favorite that will be requested next has a blinking underscore under its # number ("Next to check", a static underline with reduced motion; beta.13 drew a ring on the node being requested, but a request is too short on real hardware to see it). The Rx% tooltip says `Lifetime TX time as % of uptime · checked Ns ago` (or `not checked yet`); a checked node without data shows `—` with `No data (offline) · checked Ns ago`; EchoLink and out-of-range numbers say `Not scanned (EchoLink or private node)`, and your own nodes `Not scanned (one of your nodes)`.
- Default rate back to 20 per minute (`[favstats] rate`, still 1..25 as the ceiling, adaptive halving on 429 unchanged). The 429s seen with beta.10 and beta.11 were most likely not rate limiting: the header said "rate limited, retrying" for any global pause, and the pauses were caused by offline-node decode errors (fixed in beta.12).
- `backoffReason` in the payload says why the scan is paused: `rate-limit` (HTTP 429), `service-error` (HTTP 5xx) or `network-error` (timeout, connection error); `""` when not paused. The header shows `rate limited, retrying in Ns` only for the first, `stats service error, retrying in Ns` for the second and `paused after a network error, retrying in Ns` for the third.
- The request log line now ends `rate=<n>/min gap=<s> retry-after=<s or ->` (no token count).
- Demo mode: a request every 2 s taking 0.8 s, and some nodes report no data (offline), so progress, the ring and the offline row can be seen; `web/mock` does the same.
