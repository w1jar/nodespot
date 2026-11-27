# Schedules

Scheduled connect and disconnect of links, for example a weekly net. Added in 1.1.21; overlap check and the manual-action setting added in 1.1.24 (see "Changes in 1.1.24" below).

## Model

A schedule belongs to one local node and one target node.

| Field | Type | Notes |
|---|---|---|
| `id` | int | |
| `name` | string | optional label, max 60 chars (for example the net's name) |
| `enabled` | bool | per-schedule switch |
| `node` | string | local node; must be a current local node when saved |
| `target` | string | node number (`^[1-9][0-9]{0,9}$`), not equal to `node` |
| `action` | `connect` \| `disconnect` | |
| `mode` | `transceive` \| `monitor` \| `localmonitor` | connect only |
| `permanent` | bool | permanent link (ilink 13/12/18); the end of the window then disconnects permanently (ilink 11) |
| `exclusive` | bool | "disconnect before connect": drop other non-permanent links first, exactly like the manual Connect (ignored for local monitor) |
| `days` | int[] | weekly: days of week, 0 = Sunday … 6 = Saturday, sorted, unique |
| `date` | `YYYY-MM-DD` \| `""` | once: the date; exactly one of `days` (non-empty) or `date` is set |
| `start` | `HH:MM` | 24 h, node local time; for `disconnect` this is the disconnect time |
| `end` | `HH:MM` \| `""` | connect only, optional. End of the window: the link is disconnected then. `end` < `start` means the window crosses midnight (ends the next day). `end` == `start` is invalid |
| `lastRun` | RFC3339 \| null | last time the scheduler acted for this schedule |
| `lastResult` | string | `""`, `ok`, `skipped: <reason>` or `error: <reason>`, for display |

A one-off (`date`) schedule disables itself after its last event (the end of the window, or the start when there is no end).

## Time zone

Schedules run in the node's local time zone (`time.Local`, from `/etc/localtime` or `TZ`), so DST is handled by the OS. The API reports the zone name (IANA name when it can be found: `TZ`, `/etc/timezone`, or the `/etc/localtime` symlink target after `zoneinfo/`; else the abbreviation such as `UTC`). The UI shows schedule times as entered (node time) and notes "node time (Zone)" when the browser's zone differs. Event times are sent as RFC3339 with the node's offset; the UI shows the date/time digits of that string (node time) and computes countdowns from the absolute instant.

DST: a start time that does not exist on a given day (spring forward gap) runs at the first valid minute after it; a time that occurs twice runs once, at the first occurrence (Go's `time.Date` normalisation gives this).

## Behavior

- **Global switch.** Setting `schedules.enabled` (store settings, default `true`). While paused nothing runs. The API also reports why it is paused: `pausedReason` (`""` when paused from the switch, or e.g. `manual connect 1000 on 2001 by op`), `pausedAt` and, when the pause ends by itself, `resumeAt`.
- **Manual action.** Any connect, disconnect or disconnect-all sent through the panel's API (`POST /api/nodes/{node}/connect|disconnect|disconnect-all`, by any user) is handled according to the setting `schedules.manual_action` (since 1.1.24, see below): `pause` (default) cancels the occurrences running now and pauses all schedules until the operator turns the switch back on; `pause_today` does the same but resumes at the next local midnight; `cancel` cancels only the running occurrences on that node and keeps schedules on. Cancelled occurrences stay cancelled after re-enabling (they are not caught up); their next occurrence runs normally. Changes made outside the panel (DTMF, Allmon3, links dropping by themselves) are not detected and do not trigger it. The scheduler's own commands never do.
- **Overlap check (1.1.24).** Saving a schedule that conflicts with another is refused (`409 schedule_conflict`), see "Overlap check" below.
- **Connect with an end time (window).** At `start` the scheduler connects. At `end` it disconnects the target if it is linked (whether or not the scheduler made the link). If the target is already linked in the requested mode at `start`, the connect is skipped (`skipped: already connected`) but the window still ends with a disconnect.
- **Connect without an end, and disconnect.** Single events at `start`. A disconnect of a target that is not linked is `skipped: not connected`.
- **Catch-up.** When nodespot starts, or when schedules are re-enabled (switch or per-schedule), a connect window that is running now and has not been handled (and was not cancelled) is connected now. Windows whose end passed while nodespot was down but which the scheduler had connected are disconnected at startup (if still linked). Single events without an end are never replayed.
- **Failures.** If AMI is down, the node is unknown, or the command fails, the result is `error: …` and the scheduler retries every minute: for a window until its end, for a single event for up to 10 minutes after `start`.
- **Edits.** Saving a schedule, disabling it, or deleting it forgets its running occurrence without touching the link. If an edited schedule's window is running now and enabled, the normal catch-up applies (already linked → skipped).
- **Order.** Events due at the same minute run in the order: disconnects (window ends, then disconnect schedules), then connects; ties by schedule id. Commands go through the API's shared `rpt.Client`, so per-node pacing (one app_rpt command slot) is kept.

## Persistence

- Table `schedules` (migration 4): `id INTEGER PRIMARY KEY, name TEXT, enabled INTEGER, node TEXT, target TEXT, action TEXT, mode TEXT, permanent INTEGER, exclusive INTEGER, days TEXT` (comma-separated, `""` for once), `date TEXT, start TEXT, end_time TEXT, last_run INTEGER` (unix, 0 = never), `last_result TEXT, created INTEGER, updated INTEGER`.
- Settings keys: `schedules.enabled` (`true`/`false`), `schedules.paused_reason`, `schedules.paused_at` (RFC3339), `schedules.state` (JSON list of occurrences: `{id, start, end, status}` with `status` `connected` | `cancelled`, `start`/`end` RFC3339). Entries are removed once their end has passed (after any disconnect). Since 1.1.24: `schedules.manual_action` (`pause` | `pause_today` | `cancel`; unset or unknown = `pause`), `schedules.resume_at` (RFC3339, `""` = none) and `schedules.last_cancel` (JSON cancel notice, see below).
- Backups include schedules (the whole nodespot database); a restore drops `schedules.state` and `schedules.last_cancel` (the notice refers to that state), so a window running at backup time is not acted on. `schedules.resume_at` is kept: it belongs to the switch, which is restored with it; if its time has passed the engine resumes on its first tick.

## Engine (`internal/schedules`)

- `Engine` with injectable `Now func() time.Time`, `Loc *time.Location` (default `time.Local`), `Tick` (default 15 s), optional `Logger`, `OnChange func()`.
- Depends on small interfaces, not on `api` or `store` directly:
  - `Store`: schedule CRUD used by the engine (`ListSchedules`, `SetScheduleResult(id, at, result)`, `SetScheduleEnabled(id, bool)`), `GetSetting`/`SetSetting`.
  - `Linker`: `Connect(ctx, node, target, mode string, permanent, exclusive bool) error`, `Disconnect(ctx, node, target string, permanent bool) error`, `LinkMode(node, target string) (mode string, linked bool, known bool)` from the monitor's latest status (`known` false when the node is not a current local node).
- `Run(ctx)`: on each tick, process events in `(lastTick, now]` plus retries and catch-up as above. `lastTick` starts at the startup time (no replay of single events).
- Pure function `Upcoming(list, state, now, loc, n)` returns the next `n` events within 8 days (sorted), used by the API. It is unit-tested, including midnight-crossing windows, one-off dates in the past, DST gaps, and cancelled occurrences.
- `ManualAction(node, reason string)` (a manual link action; applies the configured mode, 1.1.24), `Pause(reason string)` (the `pause` mode: cancel running occurrences and switch off), `SetEnabled(bool)`, `SetManualAction(mode string) error`, `Changed(id)` (after an edit), `Snapshot()` for the API.
- Pure function `Conflicts(s Schedule, others []Schedule) []Schedule` and `ConflictMessage(node, list)` (overlap check, 1.1.24); unit-tested.

## API

All read endpoints: all roles. Writes: operator (and admin).

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/schedules` | | `200` Snapshot (below) |
| POST | `/api/schedules` | Schedule fields without `id`, `lastRun`, `lastResult` | `201` Schedule; `400 bad_request` with a message; `400 unknown_node`; `409 schedule_conflict` |
| PUT | `/api/schedules/{id}` | same as POST (full replace) | `200` Schedule; `404 not_found`; `409 schedule_conflict` |
| DELETE | `/api/schedules/{id}` | | `204`; `404 not_found` |
| PUT | `/api/schedules/enabled` | `{"enabled": bool}` | `200` Snapshot |
| PUT | `/api/schedules/settings` | `{"manualAction": "pause" \| "pause_today" \| "cancel"}` | `200` Snapshot; `400 bad_request` |

Snapshot:

```json
{
  "enabled": true,
  "pausedReason": "",
  "pausedAt": null,
  "resumeAt": null,
  "manualAction": "pause",
  "lastCancel": null,
  "timezone": "Europe/Berlin",
  "now": "2026-11-17T18:42:00+01:00",
  "schedules": [ /* Schedule, sorted by node, start, id */ ],
  "active": [ { "id": 3, "start": "…", "end": "…" } ],
  "upcoming": [ { "at": "2026-11-17T19:00:00+01:00", "id": 3, "name": "Tuesday net", "node": "2001", "target": "1000", "action": "connect", "mode": "transceive" } ]
}
```

`resumeAt` is set only while paused "for today" (`pause_today`). `lastCancel` is the cancel notice (`{"reason", "at", "until", "ids", "names"}`) while the cancelled windows would still be running, else `null`. `upcoming` lists the next 10 events (a window gives a `connect` and a `disconnect` event). It is computed even while paused (so the UI can show what would happen), and the UI greys it out then.

SSE event `schedules` (all roles) carries the Snapshot. It is sent after every change (CRUD, switch, pause, each executed event). The initial snapshot is fetched over REST on login, like favorites.

## UI

- **Schedules tab** (`#/schedules`), right after Dashboard, visible to all roles; write controls only for operator and admin.
  - Top card: global switch (Active / Paused, with the pause reason and time; "Paused until 00:00 (manual connect …)" when `resumeAt` is set), the "When I connect or disconnect manually" setting (select for operator and admin, plain text for viewers), the cancel notice when present, "Next: Connect 2001 → 1000 (Tuesday net), Tue 19:00, in 2 h 18 min", and the node time zone note.
  - Table: name, when (`Tue, Thu 19:00–20:30`, `Mon–Fri 07:00`, `2026-12-01 19:00–21:00`; `(+1 day)` hint for windows crossing midnight), node → target (node column only with more than one local node), mode (+ permanent / disconnect-others badges), per-schedule enabled toggle, last result, row menu (Edit, Delete with confirmation). "Running now" badge on active windows.
  - Upcoming list: the next 10 events with countdowns.
  - Add / Edit dialog: name, local node (select, only with more than one), action (Connect / Disconnect), target (NodePicker), mode, permanent, disconnect others first (default from the user's "Disconnect before Connect/Monitor" setting), repeat (Weekly with day toggles Mon … Sun, or Once with a date), start, end (connect only, optional). Client-side validation mirrors the server's, including the overlap check (inline warning, Save disabled).
- **Dashboard card**, between the connect bar and favorites: one compact line: "Schedules: Active/Paused" switch (operator), the next event with countdown (or "Running: … until 20:30"), pause reason when paused ("Paused until midnight" when `resumeAt` is set), the cancel notice when present, and a link to the Schedules tab. With no schedules it shows "No schedules" and the link only.

## Implementation notes

Decisions made while implementing where the sections above were silent.

- `schedules.Schedule` (the JSON shape above) lives in `internal/schedules`; `internal/store` imports it for its CRUD (`ListSchedules`, `GetSchedule`, `AddSchedule`, `UpdateSchedule`, `DeleteSchedule`, `SetScheduleResult`, `SetScheduleEnabled`). `Schedule.Normalize()` validates and canonicalises; the API adds the "current local node" check (`unknown_node`).
- Normalisation: a disconnect gets `mode: ""` and `exclusive: false`; `localmonitor` gets `exclusive: false`; a connect without `mode` is `transceive`; `days` are sorted and de-duplicated, and `[]` (never `null`) for a once schedule. `enabled` absent in a POST/PUT body means `true`.
- All `/api/schedules` endpoints answer `503 schedules_unavailable` when the engine is not wired (`Deps.Schedules == nil`).
- `Linker.LinkMode` reports `connecting` for a link still being established; it counts as already connected (no second connect). A link in another mode than requested is connected again (mode change), like the manual Connect.
- A window is tracked by one persisted entry (`connected`, also when the connect was skipped because the target was already linked; or `cancelled`). "A window running now without an entry" covers the start, the retries and all catch-up cases in one rule. The closing disconnect is retried every minute for up to 10 minutes after its first failed attempt; entries of a paused engine older than 10 minutes past their end are dropped.
- `Pause` keeps the reason of an earlier pause (a manual action while already paused still cancels the running occurrences). `SetEnabled` (either direction) clears the reason, drops pending single events and moves the "no replay" point to now.
- A one-off schedule is disabled once its last event is over and nothing is pending for it (including a one-off whose date is already past, without any command).
- The engine waits `StartDelay` (5 s in the server) before its first tick so the monitor has a status; before that a schedule would only record `error: node not available`.
- The `now` and `pausedAt` fields have second resolution.
- `GET /api/schedules` returns the snapshot; `active` is empty while the switch is off.

## Changes in 1.1.24

Requested after field testing.

### Overlap check

Field report: while schedule A's window was running, a new schedule B on the same node with an overlapping window and "disconnect others first" was saved; catch-up connected B and dropped A's link. Conflicting schedules are now refused at save time.

Two schedules **conflict** when all of these hold:

- same `node`;
- both `action: connect`;
- both enabled (the one being saved counts as enabled if its `enabled` is true; disabled schedules never conflict);
- at least one has `exclusive: true` (effective value: `localmonitor` is never exclusive, `Normalize` clears it);
- their time spans overlap on some actual date.

Spans: a connect with `end` covers `[start, end)`; `end` < `start` crosses midnight into the next day. A connect without `end` is an instant at `start`: it conflicts when it lies inside the other's span (`start` inclusive, `end` exclusive); two instants never conflict. Weekly against weekly is checked on a week circle, so a window spilling over midnight into the next weekday (Saturday into Sunday wraps) is covered. Weekly against once uses the date's weekday plus the spill of the previous day. Once against once compares actual dates. The arithmetic is in wall-clock minutes per weekday or date; DST edge cases (a gap or repeated hour) are ignored. Past one-off dates are not special-cased.

Where it runs: `POST /api/schedules` and `PUT /api/schedules/{id}` (the schedule itself is excluded; the per-row enabled toggle in the UI is a full PUT, so it is covered). Answer `409` with code `schedule_conflict` and a message such as `overlaps "Tuesday net" (Tue 19:00–20:30) on node 2001` (several: `overlaps "A" (…) and "B" (…) on node 2001`; an unnamed schedule is `#id`). The check and the write run under one lock. Existing conflicting schedules (saved before 1.1.24) are left alone, but saving or enabling one of them is refused until the conflict is resolved. Disabling or deleting never conflicts.

Frontend: `web/src/lib/schedules.ts` mirrors the function; `ScheduleDialog` shows the warning inline and disables Save, and shows the server's message if a 409 still comes back. The row toggle shows the server error as a toast.

### Manual action setting

Setting `schedules.manual_action`, changed with `PUT /api/schedules/settings` (operator and admin; viewers see the text). Values:

| Value | UI text | Effect of a manual connect, disconnect or disconnect-all |
|---|---|---|
| `pause` (default) | Pause all until re-enabled | As before 1.1.24: cancel running occurrences, switch off, only the operator resumes |
| `pause_today` | Pause all for today | Same, and the engine switches schedules back on at the next local midnight (`Loc`, node time) |
| `cancel` | Cancel the running window, keep schedules on | Cancel only the running occurrences (and pending retries of single events) whose schedule's `node` equals the node of the manual action; schedules stay enabled; future occurrences run normally |

Handlers call `Engine.ManualAction(node, reason)`; the scheduler's own commands do not.

`pause_today` details:

- `schedules.resume_at` (RFC3339) holds the next local midnight (computed like a schedule time, so a missing midnight is the first valid minute). On the first tick at or after it the engine switches on, clears the reason and `resume_at`, drops pending retries, and runs the same no-replay rules as `SetEnabled(true)`, with one difference: single events at or after midnight (within the usual 10 minute retry limit) do run, while those during the pause or before a restart do not. Cancelled occurrences stay cancelled, so a window that crossed midnight and was cancelled is not caught up; a window starting at or after midnight runs normally.
- `resume_at` survives a restart; if it passed while nodespot was down, the engine resumes on its first tick.
- A manual re-enable (`SetEnabled`, either direction) clears `resume_at`.
- If schedules are already paused for good (the switch, or an earlier `pause` action) a `pause_today` action does not set a resume time: an indefinite pause is never shortened. If paused for today and a `pause`-mode action arrives, `resume_at` is cleared (the pause becomes indefinite). Changing the setting never alters a pause that is already in place.

`cancel` details:

- If nothing was running (or pending a retry) on that node, nothing happens: no notice, no state change. Other nodes' windows are untouched.
- Otherwise the occurrences are marked `cancelled` in `schedules.state` (also a window running without an entry yet, e.g. while its connect was failing), pending retries of single events on that node are dropped, and a **cancel notice** is recorded in the setting `schedules.last_cancel`: `{"reason": "manual connect 1000 on 2001 by op", "at": RFC3339, "until": RFC3339, "ids": [3], "names": ["Tuesday net"]}`. `until` is the latest end of the cancelled windows (for a single event: its time plus the 10 minute retry limit). A new cancel replaces the notice. The Snapshot field `lastCancel` carries it while `until` is in the future and at least one of its schedules still exists and is enabled; the engine deletes the persisted notice once it expired, and `Changed(id)` removes that schedule from it. It survives a restart (a settings key, not derived from `state`).
- The UI reads `"Tuesday net" cancelled by manual connect 1000 on 2001 by op`.
- Window ends that were due while the link is manual are also cancelled (the closing disconnect would drop the operator's link), but only windows whose end is in the future count for the notice.
