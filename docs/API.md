# nodespot HTTP API

Contract between the Go server (`cmd/nodespot`) and the Svelte UI (`web/`). JSON everywhere, UTF-8. All paths are under `/api`. The server also serves the embedded UI at `/` (SPA: unknown non-`/api` paths return `index.html`).

## Conventions

- **Auth:** session cookie `nodespot_session` (HttpOnly, SameSite=Lax, Secure when served over HTTPS). Unauthenticated requests to protected endpoints get `401`.
- **CSRF:** every non-GET request must send header `X-nodespot: 1`; the server also rejects a non-GET whose `Origin` header doesn't match the host. Missing header returns `403`.
- **Errors:** non-2xx responses have body `{"error": "human readable message", "code": "machine_code"}`.
- **Roles:** `admin` (everything), `operator` (connect/disconnect, favorites), `viewer` (read only). Forbidden returns `403`.
- **Live updates:** Server-Sent Events (not WebSocket) at `GET /api/events`. Commands go over REST.
- Durations are integer **seconds**; timestamps are RFC 3339 strings; "never" is `null`.

## Setup and session

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/setup` | | `{"needed": bool, "hotspot": bool}`. `needed` is `true` when no users exist. `hotspot` is `true` when the request arrived through the setup hotspot (local address `10.42.0.1`), so the page can warn that the network is open. Public. |
| POST | `/api/setup` | `{"code", "username", "password"}` | `204`, and logs in. `code` is the one-time setup code the server writes to `<datadir>/setup-code`; at startup it also logs a ready-made link `http://<addr>:<port>/#/setup?code=<code>` (and prints the code in `--dev`). Only allowed while no users exist: afterwards `409 {"code":"setup_done"}`; a wrong code is `403 {"code":"bad_setup_code"}`. |
| POST | `/api/login` | `{"username", "password"}` | `200` `User`. `401` `{"code":"bad_credentials"}`. Rate limited (`429`). |
| POST | `/api/logout` | | `204` |
| GET | `/api/session` | | `200` `User` or `401` |
| POST | `/api/password` | `{"current", "new"}` | `204` |

`nodespot admin setup-code` prints the current setup code (exit 1 once an account exists; it creates the file if missing, and the server always checks against the file). Accounts can also be created out of band with `nodespot admin create` (see `nodespot admin --help`), which removes `<datadir>/setup-code`, after which `GET /api/setup` reports `needed: false`.

`User` = `{"id": int, "username": str, "role": "admin"|"operator"|"viewer"}`

### Users (admin)

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/users` | | `[User]` |
| POST | `/api/users` | `{"username", "password", "role"}` | `201` `User` |
| PUT | `/api/users/{id}` | `{"role"?, "password"?}` | `200` `User` |
| DELETE | `/api/users/{id}` | | `204` (cannot delete the last admin or yourself) |

## Nodes and links

`GET /api/nodes` returns `[NodeStatus]` for all local nodes (same objects as the SSE `node` event).

```json
NodeStatus = {
  "node": "2001",
  "info": NodeInfo | null,          // lookup for the local node itself
  "stale": false,                   // true while AMI is disconnected or polls fail
  "rxKeyed": false,                 // signal on the node's receiver (COS)
  "txKeyed": false,                 // node transmitter keyed
  "extTxKeyed": false,
  "numLinks": 159,                  // whole-network count as reported (may include duplicates)
  "links": [Link],                  // directly connected links
  "network": [{"node": "1999", "mode": "T", "kind": "allstar", "info": NodeInfo|null}],
  "flags": {"txDisabled": false, "linkFunsDisabled": false, "parrot": false, "...": "..."},
  "updated": "2026-11-15T12:00:00Z"
}
Link = {
  "node": "50241",
  "kind": "allstar" | "echolink" | "phone" | "client" | "unknown",
  "info": NodeInfo | null,
  "ip": "192.0.2.58" | "",          // only sent to admin/operator; "" for viewer
  "direction": "IN" | "OUT",
  "connected": 29,                  // seconds
  "state": "ESTABLISHED" | "CONNECTING",
  "mode": "T" | "R" | "L" | "C",    // transceive, monitor (RX only), local monitor, connecting
  "keyed": false,
  "sinceKey": 12 | null,            // seconds since this link last keyed
  "sinceUnkey": 3 | null
}
NodeInfo = {"node": "50241", "callsign": "N0CALL", "description": "Hub name", "location": "City, ST"}
```

### Link control (operator)

| Method | Path | Body | Response |
|---|---|---|---|
| POST | `/api/nodes/{node}/connect` | `{"target": "2000", "mode": "transceive"\|"monitor"\|"localmonitor", "permanent": bool, "exclusive"?: bool}` | `202` (accepted by app_rpt; the link appears via SSE) |
| POST | `/api/nodes/{node}/disconnect` | `{"target": "2000", "permanent": bool}` | `202` |
| POST | `/api/nodes/{node}/disconnect-all` | | `202` |

**`exclusive`** ("disconnect before connect", prevents cross-linking networks) is optional and defaults to `false` when absent. When `true` and `mode` is `transceive` or `monitor`, the server first sends a normal (non-permanent) disconnect (`rpt cmd <node> ilink 1 <link>`) for every current direct link of that local node except `target`, in the order of the latest monitored status (not a fresh poll), and then sends the connect. Permanent links survive because app_rpt ignores a non-permanent disconnect of a permanent link. If `target` is already directly linked it is not disconnected, but the connect is still sent (so a mode change works). Links whose name is not a valid disconnect target are skipped. If any disconnect command fails, that error is returned and the connect is not sent. `localmonitor` ignores `exclusive` (local monitor audio is not passed to other links, so it cannot cross-link).

`400 {"code":"bad_target"}` for invalid input, `404` unknown local node, `503 {"code":"ami_down"}` when AMI is disconnected.

**Manual commands and the schedules.** After a successful connect, disconnect or disconnect-all (any user), the server applies the schedules setting `manualAction` (since 1.1.24): by default it cancels the schedule occurrences running now and switches all schedules off (reason, for example `manual connect 2000 on 1997 by op`); it can instead pause only until midnight, or cancel only the running windows on that node. See Schedules below. The scheduler's own commands do not.

### Node stats (all roles)

`GET /api/nodes/{node}/stats` runs `Action: RptStatus`, `Command: NodeStat` for a local node and returns the reply's `Key: Value` lines in wire order (`Response`/`ActionID` and any `Node`/`Message` headers are dropped):

```json
{"node": "2001", "stats": [{"key": "IsRemoteBase", "value": "NO"}, {"key": "KeyupsToday", "value": "2"}, {"key": "TxTimeToday", "value": "00:00:33:172"}]}
```

`404 {"code":"unknown_node"}` for a node that is not local, `503 {"code":"ami_down"}` when AMI is disconnected.

## Settings (read: all roles; write: admin)

`Settings = {"favoritesScan": bool, "hiddenModes": [str]}` (`favoritesScan` is read-only here; it is changed with `PUT /api/favorites/scan`). `hiddenModes` lists the modes an admin hid from the Modes row and the favorite and schedule pickers (sorted, lower case; `[]` on a fresh install, never null). Hiding is display only: a hidden mode keeps its settings, can still be the active mode (the UI then shows it anyway), and favorites and schedules that use it keep working. The "Disconnect before Connect" (default on) and "Disconnect before Monitor" (default off) checkboxes are not server settings: every page load starts from those fixed defaults; a change in the connect bar lasts only until the page is reloaded (nothing is saved in the browser). The connect endpoint itself is unaffected; the UI sends `exclusive` accordingly (Connect uses the first, Monitor the second, Local Mon never). The former `exclusiveDefault` / `monitorExclusiveDefault` fields and `PUT /api/settings` were removed in 1.1.17; stored `connect.exclusive_default` / `monitor.exclusive_default` values are ignored.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/settings` | | `200` `Settings` |
| PUT | `/api/settings/hidden-modes` (admin) | `{"hiddenModes": [str]}` | `200` `Settings`. Names are lower-cased, deduplicated and sorted; a missing list, a name that is not a mode name (`[a-z0-9]`, 1–32 characters) or more than 32 names is `400 bad_request`. Unknown but well-formed names are kept (a mode a later version drops stays hidden harmlessly). `[]` shows every mode. Saved in the app's store (setting `modes.hidden`), not in the node config, so nothing is rebuilt. Audit `modes.hidden`; every client gets the SSE event `settings`. |

## Favorites (read: all roles; write: operator)

Favorites are per mode (`mode`, default `allstar`) and may be pinned to the dashboard. For AllStar the target is a node number; for EchoLink it is a node number or a callsign (`N1CALL`, `N1CALL-L`, `N1CALL-R`, stored uppercase). The Modes page sends the mode and the `ids` of that mode to `POST /api/favorites/reorder`. Writes need the operator role, not admin.

`Favorite = {"id": int, "node": "2000", "label": "My hub", "group": "Hubs" | "", "position": int, "pinned": bool, "info": NodeInfo|null}` (`pinned` favorites show as one-tap buttons on the dashboard overview; it is stored with the favorite on the server, so every browser sees the same pins, and it goes away with the favorite. New favorites are not pinned)

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/favorites` | | `[Favorite]` ordered by group then position |
| POST | `/api/favorites` | `{"node", "label"?, "group"?}` | `201` `Favorite` (label defaults to the node-list callsign/description) |
| PUT | `/api/favorites/{id}` | `{"node"?, "label"?, "group"?, "pinned"?}` | `200` `Favorite` (`pinned` is changed with the same permission as the other fields: operator or admin; viewers can read it) |
| DELETE | `/api/favorites/{id}` | | `204` |
| GET | `/api/favorites/stats` | | `200` `FavStats` snapshot (login required, all roles; never part of the public page) |
| PUT | `/api/favorites/scan` | `{"enabled": bool}` | `200` `FavStats` (operator or admin). `400` without `enabled`. Stored as setting `favstats.enabled`, default off |
| POST | `/api/favorites/reorder` | `{"ids": [int, ...]}` | `204` (positions follow array order) |
| GET | `/api/favorites/import/allscan` | | `{"found": bool, "path": str, "items": [{"node","label"}]}` preview from an existing AllScan install |
| POST | `/api/favorites/import/allscan` | | `{"imported": int, "skipped": int}` (skips nodes already in favorites) |

## Schedules (read: all roles; write: operator)

Scheduled connects and disconnects (`docs/SCHEDULES.md`). All endpoints answer `503 {"code":"schedules_unavailable"}` when the feature is not wired.

`Schedule = {"id": int, "name": str, "enabled": bool, "node": "1997", "target": "41234", "action": "connect"|"disconnect", "mode": "transceive"|"monitor"|"localmonitor"|"", "permanent": bool, "exclusive": bool, "days": [int], "date": "YYYY-MM-DD"|"", "start": "HH:MM", "end": "HH:MM"|"", "lastRun": RFC3339|null, "lastResult": str}`: `days` are 0 = Sunday to 6 = Saturday (weekly), or `date` is set (once); exactly one of the two. Times are node local time. `end` only for `connect`; `end` before `start` crosses midnight. `lastResult` is `""`, `ok`, `skipped: <reason>` or `error: <reason>`.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/schedules` | | `200` `Snapshot` |
| POST | `/api/schedules` | `Schedule` fields without `id`, `lastRun`, `lastResult` (`enabled` defaults to `true`) | `201` `Schedule`. `400 bad_request` with a message (bad target, target equals node, both or neither of `days` / `date`, bad `date`, `HH:MM` or day, `end` on a disconnect, `end` equals `start`, unknown `mode` or `action`, name over 60 characters); `400 unknown_node` when `node` is not a current local node; `409 schedule_conflict` (see below) |
| PUT | `/api/schedules/{id}` | same as POST (full replace; also how a schedule is enabled or disabled) | `200` `Schedule`; `404 not_found`; `409 schedule_conflict` (see below) |
| DELETE | `/api/schedules/{id}` | | `204`; `404 not_found` |
| PUT | `/api/schedules/enabled` | `{"enabled": bool}` | `200` `Snapshot` (the global switch; switching on clears the pause reason). `400` without `enabled` |
| PUT | `/api/schedules/settings` | `{"manualAction": "pause"\|"pause_today"\|"cancel"}` | `200` `Snapshot`. `400 bad_request` for a missing or unknown value. Operator and admin |

**Overlap check** (1.1.24): POST and PUT answer `409 {"code":"schedule_conflict","error":"overlaps \"Tuesday net\" (Tue 19:00–20:30) on node 2006"}` when the schedule being saved is a connect, enabled, on the same node as another enabled connect schedule, at least one of the two has `exclusive: true` and their time spans overlap on some date (midnight crossing and the week wrap included; a connect without `end` is an instant that conflicts only when inside the other's window). The message names the clashing schedules (`#id` when unnamed). Disabled schedules and disconnects never conflict. Details in `docs/SCHEDULES.md`.

**Manual action** (1.1.24): what a manual connect, disconnect or disconnect-all (`/api/nodes/{node}/...`) does to the schedules is set by `manualAction`: `pause` (default: cancel running windows and pause all schedules until switched on), `pause_today` (same, resumes at the next local midnight; `resumeAt` shows when) or `cancel` (cancel only the running windows on that node, schedules stay on; `lastCancel` carries the notice).

`mode` is normalised to `""` and `exclusive` to `false` for a disconnect, and `exclusive` to `false` for `localmonitor`. `permanent` on a disconnect means a permanent disconnect (`ilink 11`); on a window it also applies to the closing disconnect.

```
Snapshot = {
  "enabled": bool,            // global switch
  "pausedReason": str,        // "" when paused from the switch, else e.g. "manual connect 41234 on 2006 by op"
  "pausedAt": RFC3339 | null,
  "resumeAt": RFC3339 | null, // set while paused "for today" (pause_today): schedules switch on again then
  "manualAction": "pause" | "pause_today" | "cancel",
  "lastCancel": {"reason": str, "at": RFC3339, "until": RFC3339, "ids": [int], "names": [str]} | null,  // cancel mode: windows cancelled by a manual action, while they would still be running
  "timezone": "Europe/Berlin",  // IANA name when found, else the abbreviation
  "now": RFC3339,             // node time, with offset
  "schedules": [Schedule],    // sorted by node, start, id
  "active": [{"id": int, "start": RFC3339, "end": RFC3339}],  // windows running now (none while paused)
  "upcoming": [{"at": RFC3339, "id": int, "name": str, "node": str, "target": str, "action": "connect"|"disconnect", "mode": str}]
}
```

`upcoming` lists the next 10 events within 8 days (a window gives a `connect` and a `disconnect` event; `mode` is `""` for a disconnect), also while paused; disabled schedules and cancelled occurrences are left out. Timestamps carry the node's UTC offset. The persisted state lives in the `schedules` table and the settings `schedules.enabled`, `schedules.paused_reason`, `schedules.paused_at`, `schedules.state`, `schedules.manual_action`, `schedules.resume_at` and `schedules.last_cancel`.

## Node lookup (all roles)

| Method | Path | Response |
|---|---|---|
| GET | `/api/lookup/{node}` | `NodeInfo` or `404` |
| GET | `/api/lookup?q=text&limit=50` | `[NodeInfo]` matching node number prefix, callsign, description or location (case-insensitive) |
| GET | `/api/echolink/search?q=text&limit=50` | The EchoLink directory (not the AllStar list): `{"loaded": bool, "updated"?: RFC3339, "error"?: str, "results": [{"node": int, "callsign": str, "location"?: str, "status"?: "online"\|"busy", "kind": "user"\|"link"\|"repeater"\|"conference"}]}`. Matches an exact callsign or node number first, then callsigns starting with the text, node numbers starting with it (digits only), callsigns containing it and, for three or more characters, locations containing it; case and a conference's asterisks are ignored, an empty `q` matches nothing. `limit` 1–200 (default 50; anything else is `400 bad_request`). The node only has the directory while EchoLink is the active mode and logged in: otherwise `loaded` is false and `results` is `[]` (`error` says why when the download failed). A search never starts a mode and adds no directory traffic: it uses the list the mode already keeps (downloaded again after 5 minutes when needed). Control op `directory_search` `{mode, q, limit}`. `503` when the node is down. |
| GET | `/api/nodelist` | `{"count": int, "updated": RFC3339 \| null, "source": str, "error": str \| null}` |
| POST | `/api/nodelist/refresh` | `202` (admin) |

## Host lists (operator, admin)

| Method | Path | Response |
|---|---|---|
| GET | `/api/hostlists?mode=dmr\|ysf\|p25\|nxdn\|m17\|bm\|dmr-other` | operator: `{"mode","items":[{"value","name","detail"?,"address","port","password"?}],"truncated"?,"etag"}` with an `ETag` header and `304` on `If-None-Match`; `503 hostlists_unavailable` when the list is not downloaded yet. `bm` is the BrandMeister masters of the DMR list sorted by id (`value` the host, `name` such as `3104 – United States`, `port` 0: the native client uses 54006); `dmr-other` is the DMR list without the BrandMeister and TGIF entries |
| GET | `/api/hostlists/status` | operator: `{"busy"?, "lists": {"dmr"\|"ysf"\|"p25"\|"nxdn"\|"m17": {"entries", "updated"?, "checked"?, "error"?}}, "nextManual"?, "nodelist"?: {"count", "updated": RFC3339 \| null, "source", "error": str \| null, "busy"?}}`. `nextManual` is when a manual refresh is allowed again (absent: now) |
| POST | `/api/hostlists/refresh` | admin: downloads the host lists, then the AllStar node list, and answers the status document above. `409 hostlists_busy` with the reason when a refresh is running, when the last manual one was less than 10 minutes ago or when digital modes are off (nothing is fetched). A download that fails is not a refusal: the answer is `200` and the list's `error` says why. Audit: `hostlists.refresh`, `hostlists.refresh.refused` |

`PUT /api/modes/{mode}` (admin) saves a mode's settings `{"values": {key: value}, "favorites"?, "reflectors"?, "clear"?: [key]}`. A password that is empty or absent keeps the stored one. `clear` lists password (secret) fields to delete from the stored settings; a request with no `values` and a `clear` list changes nothing else. A key that is not a password field of the mode, or is also set in `values`, is refused with `409 node_refused`. `DELETE /api/modes/{mode}` forgets all saved settings of the mode, also while it is active (the node drops the mode first), and its remembered target.

A mode field with `"hostList": "bm"` or `"dmr-other"` in `GET /api/modes` is a text field the UI offers that list for (docs/NATIVE_NODE.md "Host lists and master pickers").

**Talker and last-heard lookup.** The node reports a talker (`mode.talker` in `GET /api/node`, the SSE event `node` and each mode's `status` in `GET /api/modes`) and the rows of `GET /api/heard` with only what the network sent; a BrandMeister call often has only `id`. The API fills the empty `callsign`, `name` and `place` from the radioid.net list (by DMR ID, then by callsign) and then the cache of hamdb.org, the same lookup as Now calling. It never waits (the radioid.net index caches the IDs it read; a callsign hamdb.org has not answered yet is only queued), never overwrites what the network sent, and skips AllStar and the echo test. `place` is "City, ST" for a US caller and "City, State, Country" otherwise. A talker nobody knows stays as sent (the UI shows "ID …"). The public view's place uses the same rule.

## System (all roles)

`GET /api/system` returns `System`; also pushed via SSE every 10 s.

```json
System = {
  "hostname": "node2001",
  "cpuTemp": 48.2 | null,          // °C
  "load": [0.12, 0.10, 0.05],
  "memTotal": 1024000000, "memAvailable": 512000000,   // bytes
  "uptime": 120660,                // host, seconds
  "asterisk": {"connected": true, "version": "22.10.1+asl3-3.10.8-1.deb12", "uptime": 120660, "error": null},
  "version": "0.1.0",              // nodespot version
  "updateAvailable": false,        // a newer package is known (cached; set by the background/manual check)
  "updateVersion": ""              // that version, "" when none
}
```

### Restart Asterisk (admin)

`POST /api/system/restart-asterisk` sends the AMI command `core restart gracefully` (Asterisk restarts once active calls end; all links drop and AMI reconnects afterwards). Response `202`. `403` for operator/viewer, `503 {"code":"ami_down"}` when AMI is disconnected. When AMI is down and configuration editing is available, the restart goes through the root helper (`systemctl restart asterisk`) instead.

`POST /api/system/asterisk/restart` does the same and answers `200 {"ok":true}`. Both are audited (`asterisk.restart`).

## Configuration editing (admin)

All routes are admin only (`403` otherwise), non-GET requests need the CSRF header, and `503 {"code":"config_unavailable"}` is returned when the helper is not reachable. Request bodies are limited to 2 MiB. Paths are relative to `/etc/asterisk`, end in `.conf`, are at most 4 levels deep, and new files are only allowed under an existing `custom/` directory. `base` is the `sha256` of the version being edited (`""` for a new file).

| Route | Body | Response |
|---|---|---|
| `GET /api/config/files` | | `[{path,size,mtime,sha256,known,group}]` (`mtime` RFC3339) |
| `GET /api/config/file?path=` | | `{path,content,sha256}` (audited as `config.read`, path only) |
| `POST /api/config/preview` | `{files:[{path,content,base}]}` | `{files:[{path,ok,error?:{line,message},diff,changed,conflict?}],reload:[{command,reason}],restart}` |
| `POST /api/config/apply` | `{files,message,reload}` | `{commit,before,files,reload:[{command,ok,output}]\|null,health:{ok,checks:[{name,ok,detail}]}\|null,restart}` |
| `GET /api/config/history?limit=&offset=` | | `[{commit,date,author,message,files}]`, newest first (`limit` 1-200, default 50) |
| `GET /api/config/history/{commit}?reveal=1` | | `{commit,date,author,message,files:[{path,status,diff}]}`; values of `secret`, `password`, `passwd`, `pass`, `md5secret`, `dbsecret` and the credentials of `register =>` are masked unless `reveal=1` (audited as `config.reveal`) |
| `POST /api/config/history/{commit}/restore` | `{reload}` | like apply, plus `skipped:[path]` |
| `POST /api/system/asterisk/restart` | | `{ok:true}` |

`reload` and `health` are `null` when `reload` was false (or nothing changed). Errors: `409 {"code":"conflict","files":[...]}` (a file changed on disk since it was loaded), `422 {"code":"syntax_error","issues":[{path,line,message}]}`, `400 bad_request`, `404 not_found`, `502 helper_error`. Audit entries (`config.apply`, `config.restore`, `config.read`, `config.reveal`, `asterisk.restart`) contain paths and commit ids only, never file contents, secret values or commit messages.

## Radio tuning (admin)

SimpleUSB audio tuning (see [SIMPLEUSB_TUNE.md](SIMPLEUSB_TUNE.md)). Every route is admin only (`403` otherwise); writes need the CSRF header. The commands run through the Asterisk CLI over the AMI connection. `503 {"code":"radio_unavailable"}` is returned when the service is not wired (it never is in a normal install). `{dev}` is the SimpleUSB device name from `simpleusb.conf` (letters, digits, `_` and `-`, at most 32); an unknown one is `404 unknown_device`. Node numbers below are placeholders.

| Route | Body | Response |
|---|---|---|
| `GET /api/radio` | | `{available,reason?,devices:[{name,node?,usbPath?,card,links:[node]}]}`. `node` comes from `rxchannel = SimpleUSB/<dev>` in rpt.conf. `available` is false (and `devices` empty) when no device exists or Asterisk cannot be asked; this never fails |
| `GET /api/radio/{dev}` | | `{device,node?,params,saved,dirty:[field],identity:{device,usbString?,serial?,card?},echoState:{on,expiresAt?}}`. `params` is the running state read with `menu-support 0+4` (exactly 16 fields, otherwise `501 unsupported_version`); `saved` is the baseline (the values of `simpleusb.conf` laid over the running ones when the device was first opened or last saved) and `dirty` the fields that differ from it |
| `PUT /api/radio/{dev}/params` | any of `{rxVoice,txA,txB,rxBoost,preEmphasis,deEmphasis,plFilter,pttInvert,carrierFrom,ctcssFrom,rxOnDelayMs,txOffDelayMs}` | `{applied:[{field,reply}],errors:[{field,message,reply?}],params,dirty}`. Levels are integers 0-999; `carrierFrom`/`ctcssFrom` are `no`, `usb`, `usbinvert`, `pp` or `ppinvert` (the driver's numeric codes are never accepted); delays are milliseconds 0-60000 and are converted to 20 ms frames. `pttInvert` true means the PTT line is "Open". Validation of the whole body happens before any command is sent (`400 invalid`). Each setting is verified against the driver's reply; a refused one is reported in `errors` |
| `GET /api/radio/{dev}/meter` | | `{pk,avg,min,max,clip,at,stale?}`: the last second of receive audio (`menu-support Y`), dBFS. At most one read per 700 ms; faster requests, and requests while another operation holds the interface, get the last reading (`stale` true). Polling it also keeps echo mode on |
| `POST /api/radio/{dev}/echo` | `{on}` | `{on,expiresAt?}`. Echo mode retransmits what the receiver hears. It switches itself off after 10 minutes, 30 seconds after the last meter or read request for the device, before a save, and when nodespot stops. Audited with the linked nodes |
| `POST /api/radio/{dev}/save` | | runs `susb tune save` (echo mode is switched off first), re-reads the device, and records `simpleusb.conf` in the config history through the helper (`config-record`): `{saved,params,changed:[field],commit?,warning?}`. `warning` explains a partial success (the history record failed, or the file does not show a running value) |
| `POST /api/radio/{dev}/revert` | | sets every tuning value back to the baseline (not `susb tune load`, which reads the interface EEPROM): like `PUT params` |

Other errors: `502 radio_unexpected_output` (a reply that is not the expected one, including a device other than the one addressed), `502 device_not_found` (the interface is not plugged in), `502 radio_command_failed`, `503 no_chan_simpleusb` (the module is not loaded), `503 radio_unavailable` (the interface is busy or the radio service is not wired), `503 ami_down`, `504 radio_unavailable` (no answer in time). Writes are audited as `radio.params`, `radio.echo`, `radio.save` and `radio.revert` with the device, the field names and the linked nodes, never reply text. Unsaved changes are lost when `chan_simpleusb` is reloaded or Asterisk restarts.

### Native node radio (admin)

The native node's radio (see [NATIVE_NODE.md](NATIVE_NODE.md), "Radio and Audio settings"). Admin only (`403` otherwise); writes need the CSRF header.

| Route | Body | Response |
|---|---|---|
| `POST /api/radio/tone` | `{seconds?}` (1 to 10; absent is 5) | `200` the node status, with `radio.testTone: true` while the tone plays. Keys the transmitter with a 1 kHz test tone at the network to radio speech level, after the key-up delay. `400 bad_request` for another length; `409 node_refused` with the node's reason when there is no radio, the node is transmitting, the receiver has a carrier, or the active mode is talking. The tone stops early when anyone else starts talking. Audited as `radio.test_tone` |
| `DELETE /api/radio/tone` | | `200` the node status. Stops the test tone; nothing to stop is not an error |
| `GET /api/radio/input` | | The live receive level `{peakDb,rmsDb,...}` plus `lastRx`, the summary of the last real transmission (see below), when there is one |
| `GET /api/radio/autoset` | | `{state,error?,result?}`. `state` is `idle`, `waiting` (no carrier yet), `measuring`, `done` or `failed`. `result` is `{step,steps,db?,speechDb,peakDb,estimated?}`: the chosen capture step, the step count, its dB when known, the measured raw speech and peak levels, and `estimated` true when the dB per step was a 1 dB fallback because the chip has no dB table |
| `POST /api/radio/autoset` | | `200` the status (`waiting`). Starts Auto-set for the receive level: the node measures raw capture, before any gain, while a carrier is active, then picks the lowest capture step whose expected speech level reaches `rxAgcTargetDb - agcMaxDb + 6` dB with the expected peak at or below -3 dBFS (no clipping wins if both cannot hold) and sets it. Failures end in `failed` with an `error`: no carrier within 15 s, less than 1.5 s of speech, or too quiet even at the highest step (nothing is changed; check the radio's volume). `409 node_refused` when there is no radio, the chip has no receive level steps, or a run is already going. Audited as `radio.autoset`. There is no transmit Auto-set: the node cannot hear what it sends |
| `DELETE /api/radio/autoset` | | `200` the status. Cancels a run; nothing to cancel is not an error |
| `POST /api/radio/tuning/dismiss` | | `200` the node status. Dismisses the "tune your audio" reminder for the radio interface in use (admin; audited as `radio.tuning.dismiss`). It sets no level; the reminder comes back if the detected interface or sound chip changes |

`radio.needsTuning` in the node status is true when a radio is present, its sound chip has a receive level control (capture steps) and the audio has not been tuned for this hardware: the dashboard then shows "Your radio audio hasn't been tuned yet." to admins. It becomes false when Auto-set succeeds, when the receive level is set by hand (`PUT /api/radio` with `rxVolume`, or a changed `radio.rxVolume` in `PUT /api/station`) or when the reminder is dismissed. The node remembers this in the config as `station.audioTuned` = `{at,interface,dismissed?}` (`at` RFC 3339; `interface` is the preset and the sound chip's USB id, `"shari:0d8c:0012"`). The node owns the record: `PUT /api/station` keeps the stored one. A config with no record counts as tuned when it already stores an audio level (`rxVolume`, `txVolume`, `rxBoost`, a static gain or an AGC target), and the node records that at its first start; a fresh config is untuned. A different interface or chip than the recorded one makes the node untuned again, a dismissal included.

`radio.lastRx` in the node status and `lastRx` in `GET /api/radio/input` hold `{at,durationMs,peakDb,speechDb,agcGainDb,outDb,agcAtMax,clipped}` for the last transmission with at least 0.5 s of speech (shorter kerchunks leave the previous one). `speechDb` is the RMS of the gated speech frames and `peakDb` the highest sample, both measured after the static receive gain and before the AGC; `agcGainDb` is the mean AGC gain over the speech, `outDb` the speech level after the AGC, `agcAtMax` true when the AGC sat at its maximum gain for most of the speech, and `clipped` the count of samples at or above 32700.

`GET`/`PUT` of the radio levels now also report `captureSteps`, `captureStep`, `captureDb`, `playbackSteps`, `playbackStep` and `playbackDb` (zero or absent when the chip has no such control; the dB values come from the control's TLV table). `rxVolume` and `txVolume` fractions are snapped to the control's own step grid: `step / steps` selects exactly that step and reads back unchanged.

`GET`/`PUT /api/station` carry the audio settings in `advanced`: `radioAgc` and `modeAgc` (AGC radio to network and network to radio, both on by default), `rxAgcTargetDb` and `txAgcTargetDb` (-40 to -6 dBFS, default -22 each), `agcMaxDb`, `rxGainDb`, `txGainDb`, `flatAudio` (software pre- and de-emphasis for a flat data port or discriminator, off by default) and `plFilter`. The retired `agcTargetDb`, `preEmphasis` and `deEmphasis` are migrated when the config is read. `radio.sa818Filter.{emphasis,highPass,lowPass}` are "on" flags; unset ones default to emphasis off, high-pass on and low-pass on.

## Backup and restore (admin)

See [BACKUP.md](BACKUP.md) for what a backup holds and how a restore works.

| Endpoint | Auth | Body | Result |
|---|---|---|---|
| `GET /api/backup/info` | admin | | `{parts:[{id:"history"\|"allmon3"\|"sounds"\|"dv",present,size}],sa818}` |
| `POST /api/backup` | admin, CSRF | `{history,allmon3,sounds,dv}` (booleans; `dv` is the digital modes part: the files of the previous stack (kept for the migration) and the private node record, with their passwords, unredacted) | the archive, `application/gzip`, `Content-Disposition: attachment; filename="nodespot-backup-<host>-<YYYYMMDD-HHMM>.tar.gz"` |
| `POST /api/restore/upload` | admin session + field `password`, or (only while setup is needed, i.e. no user exists) field `code` = the setup code | multipart, field `file` (at most 256 MiB) | `{id, preview}` |
| `POST /api/restore/{id}/apply` | admin session, or `code` in the body (same rule) | `{deleteMissing:[path], code?}` | `{ok, steps:[{name,ok,detail}], restarting, error?}` |
| `DELETE /api/restore/{id}` | as apply (`?code=` for the setup-code path) | | `204` |
| `GET /api/restore/result` | none while the result is under 30 minutes old, otherwise admin | | `{at, backupCreated, steps, health:{ok,pending,checks:[{name,ok,detail}]}, checklist:[{item,value}]}` or `404` |

`preview` is `{backup:{hostname,created,createdBy,nodespotVersion,asl3Version,asteriskVersion,arch,osId?,osCodename?,osName?,nodes,channel}, node:{asl3Version,asteriskVersion,arch,osId?,osCodename?,osName?}, warnings:[{code,message}], files:[{path,action,note?}], users:[{username,role}], favorites, missingPackages, installCommand, checklist:[{item,value}], radio:null|{band,freqRx,freqTx,tone,ctcssRx,ctcssTx,dcsRx,dcsTx,port,portPresent}, parts:{history,allmon3,sounds,sa818,dv}}`. Arrays (and `files`) are never `null`. `files[].action` is one of `restore`, `restore_dist`, `keep_stock`, `keep_new`, `delete_optional`, `skip`. `node` describes this node for comparison; the `os*` fields come from the manifest (or, for `osCodename`, from the `.debNN` version suffix) and are omitted when unknown. Warning codes: `asl3_version`, `asterisk_version`, `os_release` (backup and node run different Debian releases), `arch`, `sa818_port`, `dist_files`, `skipped_files`, `dv_settings_not_restored` (the backup was made by 1.x and holds the settings of the previous digital voice stack: 2.0 does not restore them; the previous stack packages are not in `missingPackages` or `installCommand`). `deleteMissing` may only list `delete_optional` paths of the preview.

Errors: `403 bad_password` / `bad_setup_code`, `400 bad_backup`, `409 backup_too_new` (the backup was made by a newer nodespot or has a newer format/schema), `413 too_large`, `404 not_found` (unknown or expired upload id; uploads expire after 1 hour, at most 3 are kept), `409 busy`, `429 rate_limited`, `401`/`403` for sessions, `503 helper_unavailable` / `backup_unavailable`, `502 backup_failed`.

An upload is checked completely (archive hashes, paths, the database) and the helper makes a dry run, but nothing on the node changes until `apply`. A successful `apply` answers first, then nodespot exits with status 75 and systemd starts it again (`restarting:true`); the restored database is swapped in at that start (the old one is kept as `nodespot.db.pre-restore`) and the page reads `GET /api/restore/result`; `health.pending` stays `true` until the post-restart checks have run (additive field). A failed `apply` answers `ok:false` with the steps and an `error`; the node keeps running with the old nodespot database. Audit entries: `backup.create` (parts, size) and `backup.restore`; never passwords, codes or file contents. In demo mode backup and restore run against temporary directories and nothing restarts.

## About (all roles)

`GET /api/about` returns the data of Settings > About (Asterisk version is `""` while AMI is down):

```json
About = {
  "version": "0.1.1",
  "asterisk": "22.10.1+asl3-3.10.8-1.deb12",
  "platform": {
    "os": "linux", "arch": "arm64",                       // GOOS / GOARCH
    "model": "Raspberry Pi 4 Model B Rev 1.5",            // /proc/device-tree/model, "" if absent
    "distro": "Debian GNU/Linux 12 (bookworm)",           // PRETTY_NAME of /etc/os-release
    "kernel": "6.1.0-rpi8-rpi-v8",
    "hardware": "Raspberry Pi 4"                          // value of the bug form's hardware dropdown
  }
}
```

The UI builds the bug report link from it: `https://github.com/w1jar/nodespot/issues/new?template=bug_report.yml&version=..&asl=..&hardware=..&os=..` (the query keys are the field ids of `.github/ISSUE_TEMPLATE/bug_report.yml`).

## Diagnostics (admin)

`GET /api/diagnostics` returns a `text/plain` attachment `nodespot-diagnostics-<YYYY-MM-DD>.txt` (`403` for operator/viewer). Sections: versions (nodespot, Asterisk, OS, kernel, hardware); uptime, load, memory, CPU temperature; AMI connection state; local nodes with link counts, link modes and states (no peer IPs) and the monitor's stale state; node list status; update/helper status; firewall status from the helper (when reachable); the configuration (the active lines of `nodespot.conf` and the effective values; values of `secret`, `password`, `token`, `key` entries are masked); and the last 200 lines of the server's own log (an in-memory ring of 500 lines).

The whole text is redacted before it is sent (`internal/diag/redact.go`):

- values of secret-like keys (`secret`, `password`, `token`, `key`, `authorization`, `cookie`, ...) in `k=v`, `k: v` and JSON forms, Bearer tokens, `user:pass@` in URLs, setup links (`?code=`), long random-looking tokens, and the resolved AMI secret wherever it occurs, even in free text, become `<redacted>`;
- email addresses become `<email>`;
- IPv4 and IPv6 addresses become `<ipv4>` / `<ipv6>`, except loopback (`127.x.x.x`, `::1`) and the unspecified address; MAC addresses become `<mac>`;
- callsign-like tokens (`[A-Z]{1,2}[0-9][A-Z]{1,4}`) become `<call>`, except the callsigns of the local nodes' own node-list entries. Node numbers are kept.

The hostname is not included. Redaction is best effort; users are asked to read the file before attaching it.

## Updates (admin)

Checking for updates needs no package manager. The server fetches `<[updates] url>/latest.json` (release channel) or `<[updates] url>/latest-beta.json` (beta channel) (HTTPS, 10 s timeout, 64 KB cap) and compares its `version` with the installed version using the Debian version algorithm (epoch, upstream, revision, `~` sorts first). The installed version is the running binary's version (the package version); a development build (`dev`, `v0.1.0-4-g...`, `...~git...`) asks the helper (`dpkg-query`) instead and otherwise reports "not installed from a package". A background check runs 1 minute after start and then every 6 hours (no cache buster); the result is cached. Installing is performed by the privileged helper (`nodespot-helper`, reached over a unix socket), never by the web process, and always needs an explicit `apply`. All endpoints are admin only (`403` otherwise).

```json
Updates = {
  "available": true,             // false: nothing works (see reason)
  "reason": null,                // when available is false: "not installed from a package" | "helper not running" | "no permission to use the helper"
  "applyReason": null,           // non-null: checks work but the Update button cannot (e.g. "helper not running")
  "channel": "stable" | "beta",   // update channel: the suite of /etc/apt/sources.list.d/nodespot.list as reported by the helper; "stable" when unknown
  "installed": "0.2.0", "candidate": "0.3.0", "upgradable": true,   // candidate = version in the channel's latest json ("" before the first check and after a channel change until the next check)
  "job": "idle" | "running" | "succeeded" | "failed" | "noop",       // the detached upgrade; noop: it ran but apt had nothing to upgrade
  "jobLog": "log of the latest upgrade run (at most 50 lines)",
  "notes": "release notes (plain text, - lists, **bold**) for candidate" | null,
  "checked": "2026-11-15T12:00:00Z" | null,              // last successful check since the server started
  "os": OSUpdates                                         // system (apt) packages; omitted when the server has no helper support
}

OSUpdates = {
  "available": true, "reason": null,       // false: not usable (reason as for Updates)
  "checkedAt": "2026-11-16T04:10:00Z" | null,   // last finished check, persisted across restarts
  "checking": false,                        // a check is running (apt-get update can take minutes)
  "error": null,                            // text of the last failed check, e.g. "apt is busy, try again later"
  "packages": [{"name": "libssl3", "from": "3.0.11-1", "to": "3.0.13-1", "security": true}],   // upgradable; security when the apt origin contains "security"
  "keptBack": ["raspi-firmware"],           // not installed by a plain upgrade
  "rebootRequired": false, "rebootPkgs": [],   // /var/run/reboot-required and .pkgs
  "warnings": [],                           // non-fatal notes of the last check, e.g. that the md380-emu package repository could not be refreshed (the check still succeeded)
  "job": "idle" | "running" | "succeeded" | "failed" | "noop", "jobLog": "..."   // the upgrade unit
}
```

| Method | Path | Response |
|---|---|---|
| GET | `/api/updates` | `200` `Updates`. Fast: cached state plus the helper's cheap job status (`dpkg-query` and `systemctl show`; no apt, no network). |
| POST | `/api/updates/check` | `200` `Updates`. Fetches `latest.json` now, with a `?t=<unix>` cache buster (the CDN caches it for ~10 minutes). `502 {"code":"check_failed"}` when it cannot be fetched or parsed. |
| PUT | `/api/updates/channel` | Body `{"channel":"stable"}` or `{"channel":"beta"}` (exactly these two values; anything else is `400 {"code":"bad_request"}`). Switches the update channel by asking the helper to rewrite the suite word (`stable`/`beta`) of the `nodespot` line in `/etc/apt/sources.list.d/nodespot.list`, so `apt upgrade` on the command line follows the same channel. The cached check is dropped and a fresh (cache-busted) check of the new channel's json runs. `200` `Updates`. `409 {"code":"update_running"}` while an upgrade runs, `503 {"code":"updates_unavailable"}` without helper, `502 {"code":"helper_error"}` when the file is missing or has no `nodespot` line. Switching from beta to stable never downgrades: a node with a beta installed stays on it until a release newer than it is published. |
| POST | `/api/updates/apply` | `202` `Updates` (`job: "running"`). Starts one transient systemd unit that refreshes only the nodespot apt source and runs `apt-get install --only-upgrade nodespot` (no changelog reader, no recommends); it survives the restart of nodespot. `409 {"code":"update_running"}` if one is already running. |

| POST | `/api/updates/os/check` | `202` `Updates`. Starts a check in the background (`apt-get update`, then lists upgradable packages; 5 minute timeout); poll `GET /api/updates` until `os.checking` is false. A check already running is not restarted. `409 {"code":"update_running"}` while an upgrade runs, `409 {"code":"apt_busy"}` when another apt process holds the lock ("apt is busy, try again later"), `503 {"code":"updates_unavailable"}`. |
| POST | `/api/updates/os/apply` | `202` `Updates` (`os.job: "running"`). Starts the transient unit `nodespot-osupgrade`: `apt-get update` then plain `apt-get upgrade -y` with `--force-confdef --force-confold` (never `full-upgrade`; kept-back packages stay). Survives the restart of Asterisk or nodespot if those are upgraded. Re-checks automatically afterwards. `409 {"code":"update_running"}` if any apt job (this or the nodespot update) runs. Audited as `updates.os.apply` (package names, count, security count). |
| POST | `/api/system/reboot` | `202 {"ok":true}`. The helper runs `systemctl --no-block reboot`. `409 update_running` during an upgrade, `503 updates_unavailable` without helper. Audited as `system.reboot`. |
| POST | `/api/system/restart-nodespot` | `202 {"ok":true}`. The helper runs `systemctl --no-block restart nodespot.service`; the panel is unavailable for a few seconds. Audited as `nodespot.restart`. |

All four are admin only (`403` for operator and viewer). The server also checks the OS packages once a day (the first check about 5 minutes after start when the last one is older than 24 hours); the result and its time are stored, so a restart does not trigger a new check.

Channels: the apt repository has two suites, `stable` (releases `X.Y.Z`) and `beta` (prereleases `X.Y.Z~beta.N`, plus every release). `latest.json` is the newest release; `latest-beta.json` is the newest version overall (a release newer than the newest beta is what beta nodes are offered).

`job: "noop"` means the upgrade ran but apt reported `0 upgraded`: latest.json is newer than the apt index (CDN lag). The UI says "Already up to date (the package index may lag a few minutes; try again later)". The job log only contains the latest run.

`503 {"code":"updates_unavailable"}` when updates cannot be used (see `reason` of `GET`) or the helper cannot be used for `apply`, `502 {"code":"helper_error"}` when the helper reports a failure. While the upgrade runs the server restarts, so the SSE stream drops; the UI reconnects and reloads when `hello.version` changes (also after an install outside the panel, e.g. `apt install`; once per version).

## System settings (admin)

The web UI shows these under **Settings** (`#/settings/<group>/<tab>`: System has the tabs `overview`, `updates`, `network`, `wifi`, `web-port`, `asl-services`, `terminal`; Maintenance has `backup`, `logs`, `diagnostics`, `audit`). The old `#/system/<section>` and `#/updates` links redirect to the matching group and tab, for example `#/settings/system/updates`. Every endpoint here is admin only: `403 {"code":"forbidden"}` for operator and viewer, and the Settings groups are hidden from their navigation (they see only Account and About). `503 {"code":"system_unavailable"}` without a helper, `409 {"code":"update_running"}` while an apt job runs, `502 {"code":"helper_error"}` when a helper tool fails. A missing tool (timedatectl, journalctl, systemctl, ip) is not an error: the reply sets the `*Available`/`available` flag to false. The privileged work is done by fixed helper ops (see Helper protocol); the web process never builds a command line.

| Method | Path | Notes |
|---|---|---|
| GET | `/api/system/host` | `200 HostStatus`: `hostname`, `prettyHostname`, `os`, `hostnameAvailable` (a hostname could be read; true on any node that has one), `hostnamectlAvailable` (hostnamectl works or is installed, so the hostname can be changed), `hostnamectlMissing` (it is not installed), `timezone`, `ntp` (NTP service enabled), `ntpSynchronized`, `canNtp`, `localRtc`, `timeAvailable`, `now` (RFC 3339 UTC), `clock`. |
| PUT | `/api/system/host` | Body with at least one of `{"hostname":"node2001","timezone":"America/Chicago","ntp":true}`. Hostname: one RFC 1123 label (letters, digits, hyphens; 1 to 63 characters; no leading or trailing hyphen), stored lower case. Timezone: must be in `timedatectl list-timezones`. Only values that differ are applied. `200 HostStatus` after the change, `400 {"code":"bad_request"}` for an invalid value or an empty body. Audited as `system.hostname`, `system.timezone` and `system.ntp` (`{"from":..,"to":..}`). |
| GET | `/api/system/timezones` | `200 {"timezones":[...]}`. |
| POST | `/api/system/poweroff` | `202 {"ok":true}`. The helper runs `systemctl --no-block poweroff`. The node stays off until powered on by hand. Audited as `system.poweroff`. Reboot is `POST /api/system/reboot` (above), now in the System tab. |
| GET | `/api/system/logs` | Without `unit`: `200 {"units":[...]}`, the fixed allowlist (asterisk, nodespot, nodespot-helper, NetworkManager, systemd-networkd and the digital modes units `nodespot-dv` and `md380-emu`). With `?unit=&lines=&since=`: `200 {"unit","lines","since","text","truncated","available"}`. `lines` is clamped to 50..1000 (default 200); `since` is empty, `1h`, `24h` or `boot`; anything else, or a unit not on the list, is `400 bad_request`. Output is capped at 128 KiB (newest part kept; `truncated`). |
| GET | `/api/system/services` | `200 {"available","services":[{"unit","description","active","sub","enabled","since"}]}` for the fixed unit list. |
| GET | `/api/system/network` | `200 {"available","interfaces":[{"name","state","kind","mac","mtu","addrs":[{"family","addr","prefix","scope","dynamic"}]}],"gateways":[{"family","gateway","dev","metric"}],"dns":[...],"dnsSource"}`. Read only. |
| GET | `/api/system/asl-services` | `200 SvcStatus`: `available` (systemctl works), `scopes` (one per switch `cockpit`, `apache`, `allmon3`: `scope`, `label`, `installed`, `active`, `enabled`, `recorded` (nodespot turned it off), `units` (like `services`), `firewall` (`[{service,open}]`, only with firewalld running)), `portsAvailable` (ss works), `ports` (`[{scope,label,listening,ports,addrs}]` for 80, 443, 9090, 16080 and 16700+), `nodespotPort`, `nodespotOnPort80`, `firewalld`. |
| PUT | `/api/system/asl-services/{cockpit,apache,allmon3}` | Body `{"action":"enable"\|"disable","firewall":false}`. Fixed scope and action sets; no unit name comes from the client. Only `systemctl disable --now` / `enable --now` (a static unit is only stopped or left to its socket); units are never uninstalled. `firewall` also removes (disable) or adds (enable) the scope's firewalld services (`cockpit`; `http` and `https`); a firewalld failure does not fail the change and shows in `firewallError`. `200 {...SvcStatus, "changed":[units],"firewallChanged":[services],"firewallError"?}`. `404 not_found` for an unknown scope, `400 bad_request` for a bad action, `404 not_installed`, `409 port80_held` when enabling Apache while nodespot listens on port 80. Audited as `system.svc` (`{"scope","action","units","firewall","firewallError"}`). |
| GET | `/api/system/web-port` | `200 WebPortStatus`: `available`, `listen`, `port`, `on80`, `canMove` and `reason` (`""`, `already`, `busy` (another program listens on 80), `unchecked` (ss missing), `unavailable`), `apacheActive`, `dropIn`, `previous` (the `listen` value saved before the move), `backPort`, `pending` (`{deadline,seconds}` while a move waits for confirmation), `reverted` (text when the last move was undone by the timeout), `firewall`. |
| PUT | `/api/system/web-port` | Body `{"action":"move"\|"back"\|"confirm"}`; the port is never taken from the request. `move`: refused unless port 80 is free (`409 port80_busy`, `409 port80_unchecked`, `409 already_port80`); writes the systemd drop-in `nodespot.service.d/port80.conf` (`AmbientCapabilities` and `CapabilityBoundingSet` = `CAP_NET_BIND_SERVICE`), sets the port in `listen` (the bind address is kept), opens 80/tcp in firewalld if it runs, arms a 60 s revert timer, reloads systemd and restarts nodespot with `--no-block`. The old port is not kept open. `confirm` (sent by the page once it is served from the new port) stops the timer; without it the helper moves nodespot back. `back`: removes the drop-in, restores the saved `listen` (or 8080) and restarts (`409 not_port80`). `200 WebPortStatus`. Audited as `system.webport` (`{"action",...}`); the row for `move` and `back` is written before the restart, because the answer may never reach the old address. |
| GET | `/api/system/wifi` | `200` WiFi view: `available`, `hasWifi`, `radio`, `devices:[{name,type,state,connection,addrs}]`, `active`, `ethernet`, `hotspot:{enabled,delaySeconds,active,ssid,address,firewall}`, `networks:[{id,name,ssid,priority,autoconnect,active,hidden,secured}]` (highest priority first), `viaWifi` (this request arrived on a WiFi address), `viaHotspot` (arrived through the setup hotspot). `available:false` without NetworkManager or a WiFi device; the page then shows "not available". |
| POST | `/api/system/wifi/scan` | `200 {"networks":[{ssid,signal,channel,security,secured,supported,inUse,saved}]}`. Rescans. Enterprise (802.1X) networks are listed with `supported:false`. |
| POST | `/api/system/wifi/networks` | `{"ssid","password","hidden"}`. SSID 1 to 32 bytes; password empty (open network), 8 to 63 ASCII characters or 64 hexadecimal digits. Saves the network and connects now; on failure the change is rolled back. `200 {"result":{"ssid","updated"},"wifi":WifiView}`, `400 bad_request`, `502 helper_error` (for example a wrong password). The password goes to the helper in the request body only, never in argv, logs or the audit log. Audited as `system.wifi.add` (SSID only). |
| DELETE | `/api/system/wifi/networks/{id}` | Removes a saved WiFi network (`id` is the connection UUID from the list). `200 WifiView`. Audited as `system.wifi.forget`. |
| PUT | `/api/system/wifi/order` | `{"order":[id,...]}`, every saved network exactly once, first is tried first (writes descending `autoconnect-priority`). `200 WifiView`, `400 bad_request`. Audited as `system.wifi.order`. |
| PUT | `/api/system/wifi/hotspot` | `{"enabled":bool,"delaySeconds":180}`; `delaySeconds` is optional, 30 to 3600. Persisted by the helper. Off stops and removes the hotspot profile. `200 WifiView`. Audited as `system.wifi.hotspot`. |
| GET | `/api/system/diagnostics` | `200 {"items":[{"name","desc"}],"excluded":[...],"redacted":[...],"maxBytes","timeLimit","available","maskDefault"}`: what the bundle holds, for the preview. Static: it builds nothing and works without a helper (`available` is then false). |
| GET | `/api/system/diagnostics/bundle` | The diagnostics bundle, streamed as `application/gzip` (`Content-Disposition: attachment; filename="nodespot-diagnostics-YYYYMMDD-HHMM.tar.gz"`, no `Content-Length`, `Cache-Control: no-store`). `?mask=1` (default) or `?mask=0`: replace the node's own callsigns, public IP addresses and host names (`<callsign>`, `<own-ip>`, `<own-host>`); anything else is `400 bad_request`. `409 {"code":"diag_busy"}` while another bundle is being built, `503 system_unavailable` without a helper, `502 helper_error` for a helper failure. Audited as `system.diagnostics.download` (`{"maskIdentity":bool}`) only when the bundle was built. The tarball has `README.txt` first (files, byte counts, number of secret and identity values replaced in each, what was skipped or truncated), then `report.txt`, `versions.txt`, `system.txt`, `services.txt`, `network.txt`, `audit.txt`, `journal/<unit>.txt` and `config/...`. Never included: shadow and other password files, private keys and certificates, the nodespot database and sessions, NetworkManager connection files, the Allmon3 user list. Limits: 12 MiB uncompressed, 90 s, 256 KiB per file (the newest part of a log is kept). |
| GET | `/api/audit` | `200 {"entries":[{"id","time","user","action","detail"}],"next":id\|null}`, newest first. `?limit=` 1..200 (default 50), `?before=<id>` returns entries older than that id (pass the previous `next`), `?user=` exact, `?action=` prefix. `detail` is the stored JSON text. `400 bad_request` for a bad `limit` or `before`. |

WiFi endpoints are admin only. Every mutating WiFi call is `503 {"code":"system_unavailable"}` without a helper, without NetworkManager or without a WiFi device. The setup hotspot is open (no WiFi password), has no uplink and no forwarding; the web page warns that anyone nearby can see the login.

### Browser terminal (admin)

Settings, System, Terminal (`#/settings/system/terminal`): a `login` prompt on the node in the browser. Admin only (operators and viewers get `403 forbidden`, anonymous `401`), **off by default**, switched on by an administrator. No root shell is handed out: the helper has systemd start `login` in its own transient unit, so the person still has to give a Linux account name and password (PAM decides), and the shell runs as that user.

| Method | Path | Notes |
|---|---|---|
| GET | `/api/system/terminal` | `200 {"enabled","available","sessions","maxSessions","idleMinutes","maxHours"}`. `available` is false when the helper has no terminal support. |
| PUT | `/api/system/terminal` | `{"enabled":true\|false}` -> `200` the same object. Turning it off ends every open session (end reason `disabled`). Audited as `system.terminal` (`{"from","to"}`). The setting is `terminal.enabled` in the store. |
| POST | `/api/system/terminal/ticket` | Needs the session cookie and the `X-nodespot: 1` header. `200 {"ticket","expiresIn":30}`. With the body `{"session":"<id>"}` the ticket is for reattaching to one of your own detached sessions (`404 not_found`, `403 not_yours`, `409 session_in_use`). `403 {"code":"terminal_disabled"}` while off, `429 {"code":"too_many_sessions"}` at the limit, `429 {"code":"rate_limited"}` when too many tickets are unused, `503 {"code":"system_unavailable"}` without terminal support. |
| GET | `/api/system/terminal/ws?ticket=<t>&rows=<n>&cols=<n>` | WebSocket upgrade. `rows` and `cols` are optional (default 24x80, at most 500). |
| GET | `/api/system/terminal/sessions` | `200 {"sessions":[{"id","owner","mine","started","attached","detachedSince","linuxUser"}]}`: every live session, attached or detached (admin). |
| POST | `/api/system/terminal/sessions/{id}/end` | Ends a session (an administrator may end any; audit reason `admin-ended`). `404 not_found`, `400` for a malformed id. |

**Ticket flow.** A browser cannot add the CSRF header to a WebSocket handshake, so the page first POSTs for a ticket, then opens the WebSocket with `?ticket=`. A ticket is 24 random bytes (hex), valid 30 seconds, usable once (a wrong user, wrong login session or expired ticket also burns it), tied to the user and to the login session it was issued to, and held only as a hash. At most 64 unused tickets exist at a time. The upgrade also needs the session cookie, the admin role, and an `Origin` header that names this host (or one of the extra allowed origins of `[server]`): a missing or foreign Origin is `403 {"code":"csrf"}`. A bad ticket is `403 {"code":"bad_ticket"}`; the limit of 2 concurrent sessions is `429 {"code":"too_many_sessions"}`. The Content-Security-Policy carries `connect-src 'self' ws://<host> wss://<host>` (the request's own host, only if it is a plain host name or address).

**Messages.** Browser to server: a binary message is keyboard input; a text message is JSON: `{"type":"resize","rows":R,"cols":C}` (1..500, anything else is ignored) or `{"type":"end"}`, which ends the session for good (the Disconnect button sends it; closing or reloading the page does not). A WebSocket that just closes does not end the session: it is detached and keeps running for 2 minutes, and while apt or dpkg runs in it (at most 2 hours); reattach with a ticket for `{"session"}`. On reattach the recent output (64 KiB) is replayed first. Server to browser: binary messages are the program's output. The server closes with status 1000 and the end reason as the close reason: `closed`, `idle`, `disabled`, `logout`, `limit`, `error`. Closing the WebSocket (either side) stops the session's `nodespot-term-<id>` unit in the helper (systemd ends every process of it) and closes the PTY. A restart of the web server therefore ends open sessions too.

**Limits.** 15 minutes without keyboard input ends a session (`idle`; resizes and output do not count); 8 hours is the hard maximum (`limit`); 2 concurrent sessions. The server re-checks every 5 seconds that the login session is still valid and still an admin (`logout`), and every second that the switch is on (`disabled`).

**Audit.** `system.terminal.start` (`{"ip","id"}`), `system.terminal.detach`, `system.terminal.reattach` and `system.terminal.end` (`{"seconds","reason","linuxUser","ip","id"}`; reasons `closed`, `idle`, `limit`, `disabled`, `logout`, `grace`, `apt-finished`, `admin-ended`, `error`, `exited`). `linuxUser` is the account that logged in (read by the helper from the processes of the session's unit cgroup, then utmp) or `unknown`. Keystrokes and output are never recorded anywhere, in the audit log or the logs.

## Digital modes (DMR, YSF, P25, NXDN, M17)

Design: docs/DV.md. Digital modes run on the built-in nodespot engine (`nodespot-dv`); the server talks to it over its control socket and to the root helper for the enable job and the engine config. The digital modes API moved to `/api/dv` in 2.0; the SSE event is `dv`, the backup part is `dv` and the settings keys are `dv.*`. Non-GET needs `X-nodespot: 1`. Every write is audited as `dv.*` (`dv.enable`, `dv.disable`, `dv.mode`, `dv.tune`, `dv.unlink`, `dv.favorite.*`, `dv.private.*`, `dv.node_name`, `dv.audio`, `dv.link`, `dv.resume`, `dv.lists.refresh`, `dv.userlist*`). The server also audits two actions that follow a link command of nodespot, `dv.bridge.released` (the bridge node was unlinked) and `dv.bridge.retuned` (it was linked again), as the user who made the request (`schedule` for a schedule, `system` for the permanent link); see docs/DV.md "When the bridge node is unlinked". The node connect, disconnect and disconnect-all endpoints trigger them; links made outside nodespot do not.

Errors: `503 dv_unavailable` (no helper), `503 engine_down` (the engine does not answer), `409 not_available` (the mode is not implemented in this build), `409 mode_disabled`, `409 not_enabled` (digital modes are off), `409 busy` (a call is in progress, repeat with `force`), `409 apt_busy`, `409 not_supported`, `409 clock_unset`, `409 unexpected_deps`, `400 invalid_field` (never echoes a value), `422 unknown_target` (a YSF, P25, NXDN or M17 value that no host list knows), `422 bad_config`, `502 engine_error`, `502 login_failed` (entering BrandMeister: the master refused the login or did not answer within 12 s; the mode is left; the message says why), `502 key_mismatch`, `404 no_list`, `429 too_soon`.

### Removed in 2.0

The 1.x digital modes install, setup and network write endpoints, and their helper ops, are gone; the digital modes API moved to `/api/dv`. They answer `404` (or `405` where the path still exists for another method).

### Enable and disable (admin)

| Method | Path | Reply |
|---|---|---|
| GET | `/api/dv/status` | admin | `200` DVStatus |
| GET | `/api/dv/job` | `200` `{job, log, at}`: state of the enable job (`running`, `failed`, `done` or empty) and the last 200 journal lines. Cheap, polled every 2 s while a job runs |
| POST | `/api/dv/enable` | `202` DVStatus with `job: "running"`. Installs the md380-emu vocoder (its package pulls in qemu-user-static as a dependency; on arm64 the service runs the package's own `/opt/md380-emu/qemu-arm-static`), renders the engine config, stops the units of the previous third-party digital voice stack, removes the ones nodespot installed and starts `nodespot-dv`. `409` as above; `409 not_supported` on a 32-bit (armhf) system and on unsupported releases (the status `reason` is `armhf_unsupported` for armhf) |
| POST | `/api/dv/disable` | `200` DVStatus: stops and disables `nodespot-dv`; settings stay |

```json
DVStatus = {
  "supported": true, "reason": "...",          // reason only when supported is false
  "md380Installed": true, "enabled": true, "active": true, "md380Active": true,
  "job": "running",                            // omitted when there is no job
  "legacyUnits": ["analog_bridge.service"]      // units of the previous stack that still exist
}
```

### Setup (admin, read only)

`GET /api/dv/setup` -> `{"bridgeNode": {node, owned, suggested, used, localNodes, rxPort, txPort, link:{mode, localNode, bridge, exclusive}, paused, name}, "files": [{"id": "rpt_conf", "present": true, "writable": true, "sha256": "...", "history": 2}, {"id": "modules_conf", ...}]}`. There are no setup writes any more: identity, credentials, modes, networks, BrandMeister and the engine's own files are not edited through nodespot in 2.0. `POST /api/dv/rollback` (and `/private-node/rollback`) undoes the last private node change: `200 {rolledBack, problems:[], steps:[]}`, `404 no_rollback`.

### Panel (snapshot, mode, tune, unlink, favorites)

| Method | Path | Role | Body | Reply |
|---|---|---|---|---|
| GET | `/api/dv` | viewer and up | | `200` DVSnapshot (`installed`, `modes`, ...) |
| POST | `/api/dv/mode` | operator, admin | `{"mode":"LOOP\|BM\|TGIF\|DMR\|YSF\|P25\|NXDN\|M17","force"?}` | `200` `{mode, switched, confirmed, left?, snapshot}`. Switches the mode; the remembered tune of that mode is sent again, if there is one |
| POST | `/api/dv/tune` | operator, admin | `{"mode":"LOOP","tune":"9990","force"?}` | `200` as mode. DMR networks take a talkgroup, YSF a reflector id (or `host:port`), P25 and NXDN a talkgroup, M17 a reflector and module as `M17-XXX C` (designator `M17-` plus three upper-case letters or digits, one space, a module `A`-`Z`; the reflector is looked up in the M17 host list, `422 unknown_target` when it is not there). `LOOP` (the Echo test) has no tune: switch to it with `/api/dv/mode`; the old `9990` value is still accepted |
| POST | `/api/dv/unlink` | operator, admin | `{}` | `200` as mode. YSF and M17 `disconnect`, DMR networks `4000`, P25 and NXDN `9999` |
| GET | `/api/dv/favorites` | viewer and up | | `200` `[{id,mode,value,name,sort,pinned}]` |
| POST | `/api/dv/favorites` | operator, admin | `{mode,value,name}` | `201` favorite; `400 bad_request` |
| PUT | `/api/dv/favorites/{id}` | operator, admin | `{name?,sort?,pinned?}` | `200` favorite (`pinned` puts it on the dashboard overview, like the AllStar `pinned`; new favorites are not pinned); `404 not_found` |
| DELETE | `/api/dv/favorites/{id}` | operator, admin | | `204`; `404 not_found` |
| GET | `/api/dv/resume` | operator, admin | | `200` `{"resume":false,"lastMode":"BM","perMode":{...}}` |
| PUT | `/api/dv/resume` | admin | `{"resume":true}` | `200` as GET; `400 bad_request` |

`LOOP` is the Echo test (docs/DV.md "Echo test"), a local audio test through the bridge node: it is always enabled and available, the snapshot's `bridge.tune` is empty while it runs, and its playback is a call from "Echo test" with no `dst`. The Digital modes setup page starts it with `POST /api/dv/mode` and stops it by switching back to the previous mode. A mode the engine does not implement answers `409 not_available` to mode and tune, and its `available` flag in the snapshot is `false`; their settings and favorites are kept. A mode switched off in the engine config answers `409 mode_disabled`.

```json
DVSnapshot = {
  "installed": true, "running": true, "configured": true,
  "bridge": {"mode": "BM", "tune": "91", "slot": 2, "muted": false, "source": "nodespot-dv", "updated": "2026-11-22T00:49:17Z"},   // source "none" while disabled
  "modes": [{"id": "LOOP", "enabled": true, "available": true, "unlink": "disconnect", "label": "Echo test"},
            {"id": "BM", "enabled": true, "available": false, "unlink": "4000", "label": "BrandMeister"}],
  "services": [{"unit": "nodespot-dv", "active": true}, {"unit": "md380-emu", "active": true}],
  "vocoder": {"ok": true, "timeouts": 0},
  "engine": {"version": "2.0.0", "uptime": 3600},
  "bridgeNode": {"present": true, "node": "1999", "name": "Digital modes linked node"},
  "link": {"mode": "ondemand", "localNode": "1998", "linked": true, "paused": false, "exclusive": false},
  "caller": {"id": 7, "call": "N0CALL", "name": "...", "dmrId": 1234567, "tg": 9, "at": "2026-11-22T00:49:17", "...": "..."},
  "heard": [ /* the last 10 callers */ ]
}
```

SSE event `dv` carries the snapshot (all roles), sent only when it changed.

### Now Calling and the radioid.net user list

| Method | Path | Role | Reply |
|---|---|---|---|
| GET | `/api/nowcalling` | viewer, operator, admin | `200` `{current: Call \| null, recent: [Call]}` |
| GET | `/api/dv/userlist` | admin | `200` `{enabled, entries, updated?, checked?, error?, busy?, size?}` |
| PUT | `/api/dv/userlist` | admin, CSRF | `{"enabled": bool}`; `200` the status. Turning it on with no list downloads it. Audited as `dv.userlist` |
| POST | `/api/dv/userlist/download` | admin, CSRF | `202` the status (the download runs in the background; poll the GET while `busy`). `409` `userlist_off` or `userlist_busy`. Audited as `dv.userlist.download` |

`Call` is `{id, kind: "allstar"\|"dmr"\|"ysf"\|"m17", call?, first?, name?, city?, state?, country?, location?, dmrId?, node?, desc?, local?, network?, tg?, tgName?, slot?, private?, room?, start, end?, active}`; times are RFC 3339 UTC. Only one talker is `current` (the most recently keyed); `recent` holds the last finished calls, newest first, at most 5. The state is kept in memory on the server, so it survives a page reload but not a restart of nodespot. Sources: the keyed adjacent AllStar link (the bridge link excluded), and for the digital side the previous stack's logs: `STFU.log` (BrandMeister: `ODMR Begin Tx` and `End Tx` lines, 60 ms per frame, talker alias lines ignored) and `Analog_Bridge.log` (`Begin TX:` lines, also for YSF, where `call=` is the callsign). the previous stack calls count only while the bridge node is linked to a local node (a stale monitor state does not count as linked): while it is not linked, a talk in the logs is neither current nor recorded, a talk that started before the link came up is never shown (the logs are re-read on every poll), and a digital call that is active when the link drops ends at that moment and goes to `recent` like a finished call. With no end line, the bridge link's keyed state ends the call, 5 seconds after the link unkeys (a call whose link keys again within that time goes on; its end is the moment the link first unkeyed). AllStar calls use the AllStar node list only (`call`, `desc` and `location` of the keyed node; the radioid.net list is never consulted and `first`, `name`, `city`, `state` and `country` stay empty). For M17 calls `room` is the reflector and module (`M17-XXX C`) and there is no `tg` or `slot`. For DMR and YSF, name and place come from the radioid.net list (DMR by ID, YSF by callsign; M17 by callsign), then `DMRIds.dat` (callsign only), then the bare callsign. Callsigns, names and DMR IDs are for logged-in roles only, never audited. `userlist_unavailable` (`503`) when the list is not available.

#### hamdb.org lookup

| Method | Path | Auth | Notes |
|---|---|---|---|
| GET | `/api/dv/hamdb` | admin | `200` `{enabled, cached, today, dailyMax, pausedUntil?}`; `503 hamdb_unavailable` |
| PUT | `/api/dv/hamdb` | admin, CSRF | `{"enabled": bool}`; `200` the status. Audited as `dv.hamdb` |

Off by default (setting `hamdb.enabled`, kept in the database and so in backups). When on, a YSF, P25, NXDN or M17 caller (no DMR ID) whose callsign looks like an amateur callsign (3 to 10 letters and digits, at least one of each) and is not in the radioid.net list gets name and place from `https://api.hamdb.org/v1/<CALL>/json/nodespot` (the unversioned path only redirects). The lookup never blocks: a callsign not in the cache is queued, and the name appears on a later snapshot (`GET /api/dv/snapshot` callers have `name` and `place`; Now Calling calls get `first`, `name`, `city`, `state`, `country` and `source: "hamdb.org"`). AllStar calls are never looked up. Limits: one request in flight, at least 10 s apart, at most 100 in 24 hours (kept over a restart), a queue of 20 that drops the rest, 5 s timeout, redirects not followed. A 429 or 503 is waited out (`Retry-After`, else 30 s doubling with jitter, at most 1 h); 5 failures in a row pause requests for 1 h. Answers are cached in `hamdb-cache.json` in the data directory: found 30 days, not found 7 days, at most 5000 entries (oldest dropped). `User-Agent: nodespot/<version>` only.

The list is the CSV `https://radioid.net/static/user.csv` (header `RADIO_ID,CALLSIGN,FIRST_NAME,LAST_NAME,CITY,STATE,COUNTRY`), on by default, checked daily with `If-Modified-Since`/`ETag` (an unchanged list answers `304`). The file `user.csv` is kept in the data directory (atomic replace; a download with a wrong header, an error or more than 64 MB keeps the old file) with a compact index in memory (about 12 bytes per row, rows are read from the file on lookup). The switch is the setting `radioid.enabled`. `User-Agent: nodespot/<version>`.

SSE event `nowcalling` carries `{current, recent}` (all roles; also sent on connect), whenever it changes.

### Digital modes private bridge node (admin)

Design: docs/DV.md "Private node and USRP". Admin only (`403 forbidden`), `X-nodespot: 1`.

| Method | Path | Body | Reply |
|---|---|---|---|
| PUT | `/api/dv/link` | `{"exclusive": bool}` (operator, admin) | `200` `{exclusive}`; saves "Disconnect other links first" (`dv.link_exclusive`) |
| PUT | `/api/dv/private-node` | `{"node","rxPort","txPort","linkMode":"ondemand\|permanent","localNode","linkExclusive"?,"dryRun"?,"restart"?,"base"?}` | `200` plan (dry run) or result `{ok, steps:[...], rollback}`. Steps: `disconnect`, `asterisk_config`, `engine_config`, `asterisk_restart`, `engine_reload`, `health` |
| DELETE | `/api/dv/private-node` | optional `{"dryRun"?,"restart"?,"base"?}` | removes the bridge node nodespot added; same step list |
| GET | `/api/dv/audio` | admin | `200` `{agcTx,agcRx,agcTargetDb,agcMaxGainDb,gainRxDb,gainTxDb}`: the audio settings (defaults `false,true,-22,18,0,0`) |
| PUT | `/api/dv/audio` (admin) | the same fields; one left out keeps its default. `agcTargetDb` -40..-6, `agcMaxGainDb` 0..30, gains -20..20 | `200` the settings, `applied`, `applyError?`; hot reload, no restart. `400 invalid_field` |
| GET | `/api/dv/meter` | viewer | `200` `{running, audio?:{targetDb, tx, rx}}`; `tx` is the node audio to the network, `rx` the network audio to the node. Each: `{agc, active, peakDb, rmsDb, outPeakDb, outRmsDb, last?:{speechDb, outSpeechDb, peakDb, outPeakDb, clipped, durationMs, verdict:"low"\|"ok"\|"high", agc, at}}`. Cached 400 ms |
| PUT | `/api/dv/private-node/name` | `{"name": string}` | `200` `{name}`; the default is "Digital modes linked node" (`dv.node_name`) |
| POST | `/api/dv/private-node/adopt` | `{}` | `200` `{adopted,node,chanUsrpWas}`: saves nodespot's record for a bridge node that already exists |
| POST | `/api/dv/private-node/rollback` | `{}` | `200` `{rolledBack,problems:[],steps:[]}`; `404 no_rollback` |

The rollback state is `{available, before?, ini?}`. The engine config is rendered by the helper (`dv-config`) and the engine re-reads it with a `reload` on its control socket; if the socket does not answer, the helper restarts the unit.

### Digital modes networks and identity (admin)

Design: docs/DV.md "BrandMeister". Admin only (`403 forbidden`), `X-nodespot: 1` on writes. The root helper (`dv-network`) writes `networks.json`, renders the engine config and the server then sends `reload` to the engine. The password is write-only: no reply, log line or audit entry carries it; the views say only `passwordSet`.

| Method | Path | Body | Reply |
|---|---|---|---|
| GET | `/api/dv/networks` | | `200` `{identity, modes:[NetView]}` in display order (`BM`, `TGIF`, `DMR`, `YSF`, `P25`, `NXDN`, `M17`) |
| GET | `/api/dv/networks/{mode}` | | `200` NetView; `404 not_found` for a mode without settings |
| PUT | `/api/dv/networks/{mode}` | any of `{"enabled","address","port","password","clearPassword","options":{}}` | `200` NetView plus `applied` and `applyError?`. `options`, when sent, replaces all options; an empty value drops a key. `port` 0 restores the default. A `password` of `""` keeps the stored one |
| GET | `/api/dv/identity` | | `200` `{callsign, dmrId, repeaterId?}` |
| PUT | `/api/dv/identity` | `{"callsign","dmrId","repeaterId"?}` | `200` the identity plus `applied`. An empty callsign and DMR ID clear it, which also disables the networks that need one. The panel sets `repeaterId` to DMR ID × 100 + the ESSID it shows, and omits it when the ESSID is empty. |

```json
NetView = {
  "mode": "BM", "label": "BrandMeister", "available": true, "enabled": true,
  "address": "3104.master.example.net", "port": 0, "defaultAddress": "", "passwordSet": true,
  "fields": {"address": true, "port": false, "password": true}, "needsIdentity": true, "help": "...",
  "options": [{"key": "client_version", "label": "Software description", "kind": "text", "default": "nodespot-dv", "advanced": true, "help": "..."}],
  "values": {"client_version": "nodespot-dv"},
  "login": "ok", "detail": ""               // engine login state of an enabled, available mode: off, pending, ok, failed, idle (BrandMeister while another mode is active: no session on purpose, shown as "Not in use")
}
```

Validation (`422 invalid_field`, the message names the field, never a value): callsign 3 to 8 letters and digits (stored upper case); DMR ID 7 digits (1000000 to 9999999); repeater ID 0 or 1000000 to 999999999; port 1 to 65535; address a host name or an IP address; password 1 to 64 printable characters without blanks; option keys only from the per-mode table (`dvpanel.NetOptions`) and values within the spec. `422 incomplete`: enabling a network needs its address (unless the mode has a `defaultAddress`), its password (not NXDN, which has none) and, for a DMR or NXDN network, the identity; NXDN also needs `options.nxdn_id` (an enable request without it is `422 incomplete`; saving with `options.nxdn_id` cleared while NXDN is enabled succeeds and turns NXDN off). `options` replaces all options of the mode: `{}` clears them all, an empty value drops that key. Text options carry an optional `maxLen`; bool options take `true` or `false`. `applied: false` with `applyError`: the settings are saved but the engine did not take them (digital modes off, or the engine refused the file).

`M17` is enable-only like `YSF` (`fields.address`, `fields.port` and `fields.password` false; an `address`, `port` or `password` in a `PUT` is `422 invalid_field`) and needs only the identity's callsign (the identity form still asks for the DMR ID). Its options are `can` (channel access number 0 to 15, default 0), `suffix` (up to 3 characters of letters, digits, `-`, `/` or `.`, appended to the callsign, for example `R`: the node is then `N0CALL R`), and the timers `ack_timeout_ms` (2000), `timeout_ms` (30000) and `rx_timeout_ms` (1500). The reflector and module are not settings: they come from the tune. An enable request is `422 incomplete` when the callsign plus the suffix does not fit the 9 characters of an M17 callsign; changing the identity to such a callsign keeps M17 off in the engine configuration. `BM` has the option `stay_logged_in` (bool, default false: BrandMeister logs in only while it is the active mode; `"true"` keeps the session in every mode; docs/DV.md "BrandMeister"). `BM` has `fields.port: false`: a `port` in `PUT /api/dv/networks/BM` is refused (`422 invalid_field`) and a port stored by an earlier version is dropped on load. `TGIF` has `fields.address` and `fields.port` false as well (its master comes from the DMR host list, else `tgif.network:62031`), no `options` or `slot` option (always timeslot 2, no RPTO): an `address`, `port`, `options.options` or `options.slot` in a `PUT` is refused (`422 invalid_field`) and the same values stored by an earlier version are dropped on load. `YSF` and `P25` are enable-only (`fields.address` and `fields.port` false, no `p25_id` option): an `address`, `port` or `options.p25_id` in a `PUT` is refused (`422 invalid_field`), and the same fields stored by an earlier version are dropped on load. The snapshot's `modes[]` gains `login` and `detail` (an enabled, available network's login state). `available` comes from the engine (`Status.networks[].available`); while the engine is down it falls back to the static list of native modes in the build (`LOOP`, `BM`, `TGIF`, `DMR`, `YSF`, `P25`, `NXDN`, `M17`). Audit: `dv.network` (mode and the names of the changed fields) and `dv.identity`.

### Digital modes host lists (operator)

| Method | Path | Reply |
|---|---|---|
| GET | `/api/dv/lists?mode=dmr\|ysf\|p25\|nxdn\|m17\|bm\|dmr-other` | `200` DVList `{"mode","items":[{"value","name","detail"?,"address","port","password"?}],"truncated"?,"etag"}`, `ETag` header, `304` on `If-None-Match`. `404 no_list` when the list is not downloaded yet. operator and admin. `mode=bm` is the BrandMeister masters of the DMR list (names `BM_<id>_<Country>`), sorted by id: `value` is the master host name, `name` the label (`3104 – United States`), `port` is 0 (BrandMeister has no port setting: the engine uses 54006). `mode=dmr-other` is the DMR list without the BrandMeister (`BM_`) and TGIF entries, in list order, one per host; `password` is the list's (shared) password for the master. `GET /api/dv/networks` gives DMR - Other a `defaultPassword` (`passw0rd`), used at login when no password is stored, so it can be enabled without one |
| POST | `/api/dv/lists/refresh` | admin: starts a download now. `200` the list status; `429 too_soon`, `409 busy`, `409 not_enabled` |

nodespot downloads the five lists itself, once a day, only while digital modes are enabled. `value` is what a pick writes: the master address (dmr), the 5-digit reflector id (ysf), the TG (p25, nxdn) or the reflector designator `M17-XXX` (m17; `name` is the same, `address` and `port` the reflector's, `detail` is empty). The M17 list has no modules: the tune is `value` plus a space and the module the operator picks, for example `M17-XXX C`. Favorites (`POST /api/dv/favorites`) take the same value for `mode` `M17`, module included.

## Public status view

Optional, off by default (admin setting). Served on the main port. While disabled the three public endpoints answer `404 {"code":"not_found"}`. They need no session and no CSRF header (GET only), set no cookies, and are rate-limited per client address (`429 {"code":"rate_limited"}` with `Retry-After`). The data comes from dedicated structs: it never contains IP addresses, system information, favorites or users.

| Method | Path | Response |
|---|---|---|
| GET | `/api/public/info` | `{"enabled": bool, "title": str, "loggedIn": bool}`. Always answers; `title` is empty while disabled; `loggedIn` says whether the request carries a valid session. |
| GET | `/api/public/status` | `PublicSnapshot` |
| GET | `/api/public/events` | SSE (below). `429` over the per-address cap (4 connections), `503` over the global cap (200). |

```json
PublicSnapshot = {
  "title": "N0AAD AllStarLink node 2001",   // setting, else "<callsign> AllStarLink node <n>", else "AllStarLink node status"
  "now": 1794725912,                          // server time, unix seconds
  "callsigns": true, "showNetwork": true, "showActivity": true, "showDV": false,
  "nodes": [{
    "node": "2001", "info": {"callsign","description","location"} | null,
    "stale": false, "txKeyed": false, "rxKeyed": false, "numLinks": 27,
    "links": [{"node": "50241", "kind": "allstar", "info": {...} /* omitted when callsigns are hidden */,
               "direction": "OUT", "mode": "T"|"R"|"L"|"C", "connected": 64, "keyed": false,
               "sinceKey": 12 | null, "sinceUnkey": 3 | null}],
    "network": [{"node": "1021", "callsign": "N0AAA" /* omitted when hidden */}]   // omitted when disabled or empty
  }],
  "activity": [{"node": "2001", "link": "50241" | "local", "callsign"?: "N0AAF", "at": 1794725900, "duration": 7, "kerchunk": false}],   // newest first, at most 25; omitted when disabled
  "dv": {"now": DVCall | null, "heard": [DVCall]}   // only while showDV is on; omitted otherwise
}
DVCall = {"mode": "DMR"|"YSF"|"P25"|"NXDN"|"M17", "callsign"?, "target"?: "TG 91" | "YSF-TEST", "slot"?: 2, "at": 1794725900,
          "name"?, "location"?}   // callsign, name and location omitted when callsigns are hidden
```

`dv` is an allowlist and exists only while the admin setting `showDV` ("Show digital modes activity", default off) is on; with it off no digital modes data is sent. `heard` has at most 10 calls, newest first; private calls are dropped. `name` and `location` come from the local radioid list or the HamDB cache only: a public request never causes a lookup. It never contains a master or server host, login state, bridge node or link state, DMR ID, identity or settings, address or version. The bridge node's own rows in `activity` carry `label` (its display name).

Ages (`connected`, `sinceKey`, `sinceUnkey`) are as of `now`; the page advances them locally. Link nodes that are not node numbers, callsigns or EchoLink numbers (an address, for example) are published as `"unknown"`.

SSE events: `snapshot` (`PublicSnapshot`; on connect, after a stale/recovered transition, and when an admin changes the settings), `node` (one node without `network`), `network` (`{"node", "network": [...]}`), `activity` (`{"now", "activity": [...]}`), `dv` (`{"now", "dv": {...}}`, only while `showDV` is on; coalesced to at most about one per second). A comment line (`: ping`) is sent every 20 s and a stream is closed after 6 hours (the page reconnects). If SSE is unavailable the page polls `/api/public/status` every 10 s.

### Public page settings (admin)

`PublicSettings = {"enabled", "title", "nodes": [str], "showCallsigns", "showNetwork", "showActivity", "showDV", "allowEmbedding", "trustProxy", "defaultTitle", "localNodes": [{"node","info"}], "clients": int}`. Defaults: disabled, empty title (automatic), `nodes: []` (all local nodes), callsigns/network/activity on, digital modes activity (`showDV`) off, embedding off, trust proxy off. Stored as settings `public.*`.

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/public` | | `200` `PublicSettings` (admin) |
| PUT | `/api/public` | any subset of `enabled`, `title` (at most 80 characters, no control characters), `nodes` (local node numbers; all of them or `[]` means all), `showCallsigns`, `showNetwork`, `showActivity`, `showDV`, `allowEmbedding`, `trustProxy` | `200` `PublicSettings`; `400 {"code":"bad_request"}` for invalid input. Applies immediately to connected visitors; disabling closes their streams. |

`trustProxy`: use the last `X-Forwarded-For` entry as the client address for the rate limit and connection caps (only behind a reverse proxy or tunnel that sets it; otherwise clients could forge it).

**Embedding.** `/embed` serves the same app as a chrome-less public view. Only `/embed` gets `Content-Security-Policy: frame-ancestors *` (and no `X-Frame-Options`), and only while the page is enabled and `allowEmbedding` is on. All other paths, the login page included, send `frame-ancestors 'none'` and `X-Frame-Options: DENY`.

### Activity (all roles)

`GET /api/activity?node=&limit=` returns recorded transmissions, newest first (`limit` 1-500, default 50; `node` filters by local node): `[{"id", "node", "link": "50241"|"local", "info": NodeInfo|null, "start": RFC3339, "duration": 7.4 /* seconds */, "kerchunk": false}]`. The server records a row when a link or the local receiver unkeys (keyups under 0.5 s are ignored, under 1.5 s are flagged `kerchunk`); rows older than 7 days are deleted daily and the table is capped at 10,000 rows.

### Favorites activity scan

`FavStats = {"enabled": bool, "rate": int, "effRate": int, "backoff": bool, "retryAt": RFC3339 | null, "backoffReason": "rate-limit" | "service-error" | "network-error" | "", "updated": RFC3339 | null, "checking": "<node>" | "", "next": "<node>" | "", "nextAt": RFC3339 | null, "checked": int, "total": int, "checkedAt": {"<node>": RFC3339}, "stats": {"<node>": Activity}, "removed": ["<node>"]}` (`removed` is always `[]` in the snapshot; `checking` is the node being requested now (kept for compatibility), `next` the node that will be requested next (same ordering as the poller; `""` when off, nobody is watching, nothing is eligible or all are in their skip period), `nextAt` when the next request is due, `total` the eligible favorites, `checked` how many have an answer in the current pass, `checkedAt` each node's last attempt including offline ones; `updated` is kept for compatibility) with `Activity = {"rxPct": int | null, "linkCnt": int, "active": bool, "webTx": bool, "keyed": bool, "txAvg": int, "lastSeen": RFC3339}`. See `docs/FAVSCAN.md`.

One server-side poller queries `https://stats.allstarlink.org/api/stats/<node>` (base URL: `[favstats] url`) for each distinct favorite in 2000..999999 (regular node numbers; EchoLink numbers 3000000..3999999 and private numbers below 2000 are skipped and show no values) that is not one of this machine's nodes, round robin, one request at a time. It runs only while the scan is on and at least one logged-in SSE client is connected (public-page visitors do not count); values are kept 10 minutes after the last viewer leaves. Requests are evenly paced, one every 60/rate seconds from the first one (`[favstats] rate`, default 20 per minute = one every 3 s, at most 25; never closer than 3 s) and paused with exponential backoff (15 s up to 5 min, `Retry-After` honored) after HTTP 429 or an error. The rate is adaptive: each 429 halves the rate in use (`effRate`, not below 3 per minute) and every 20 consecutive successful requests raise it by 1 per minute, up to `rate`. A 429 never replaces or clears a node's values. Nodes never queried go first, then the one queried longest ago. `retryAt` is when the current backoff ends (`null` otherwise). `[favstats] log = true` logs one line per request (node, status, duration, rate, gap, Retry-After) and each rate change to the journal. `rxPct` is `round(100*totaltxtime/apprptuptime)` and is `null` unless uptime is over 60 s and TX time over 10 s. `keyed` is derived from changes of `totalkeyups` / `totaltxtime` between polls (the API's own `keyed` flag is only used for the first sample); counter resets and implausible jumps count as no activity. `txAvg` is a moving average of the recent TX share. `active` / `webTx` are the node's own `Status == "Active"` and `access_webtransceiver`. `backoff` is true while polling is paused and `backoffReason` says why (`rate-limit` = HTTP 429, `service-error` = HTTP 5xx, `network-error` = timeout or connection error; `""` otherwise); `updated` is the time of the last successful poll. Only the fields above are read from the response; it is never stored, logged or forwarded.

## Server-Sent Events: `GET /api/events`

Every `/api/` response carries `X-nodespot-Instance`, a random id generated when the server process starts (also sent as `hello.instance`); the UI compares it to detect that the panel restarted.

Requires a session. Each message has an `event:` name and a JSON `data:` line. On connect the server sends `hello`, then a full state; afterwards only changes.

| event | data |
|---|---|
| `hello` | `{"user": User, "version": str, "instance": str, "settings": Settings}` |
| `settings` | `Settings`: the app settings changed (for now, hidden modes); all roles |
| `nodes` | `[NodeStatus]`: full state (sent on connect and after AMI reconnects) |
| `node` | `NodeStatus`: one node changed |
| `system` | `System` (including `updateAvailable` / `updateVersion`) |
| `updates` | Admin clients only: `{"nodespot": bool, "nodespotVersion": str, "os": n, "security": n, "reboot": bool}`: counts for the header badge, never package names. Sent on connect and whenever they change. |
| `favorites` | `[Favorite]`: favorites changed (any client) |
| `nowcalling` | `{current, recent}`: the Now Calling strip (see "Now Calling and the radioid.net user list"); sent on connect and when it changes |
| `schedules` | `Snapshot` (see Schedules; all roles): sent after every change (create, edit, delete, switch, pause) and after each executed event. Not part of the initial state: fetch `GET /api/schedules` after login. |
| `favstats` | `FavStats` with only the changed nodes in `stats` and, in `removed`, the nodes whose values were just cleared (offline, no data, 404, no longer a favorite; clients delete them and show an empty row): sent when a request starts (`checking` set, `next` moved on) and when it ends, and whenever the favorites change the `next` node (`checking` cleared, the node's `checkedAt` and the progress updated; `stats` only if its values changed), and when the scan is switched on or off (then `enabled` says which; off clears all values). `checkedAt` also carries only the changed nodes. Clients merge it into the snapshot. |

The server sends a comment line (`: ping`) every 15 s. While at least one client is connected the server polls node status about once per second; otherwise it slows down.

## Configuration file

`/etc/nodespot/nodespot.conf`, Asterisk-style INI (parsed with `internal/astconf`):

```ini
[server]
; ":PORT" or "ADDR:PORT"; the default port is 8080
listen = :8080
datadir = /var/lib/nodespot

[ami]
; "auto" reads the first user from /etc/asterisk/manager.conf
host = 127.0.0.1
port = 5038
username = auto
secret = auto

[nodelist]
url = https://allmondb.allstarlink.org/
refresh = 24h

[helper]
; unix socket of the privileged helper (nodespot-helper.socket)
socket = /run/nodespot/helper.sock

[favstats]
; favorites activity scan: base URL of the stats API and requests per minute (1..25, default 20), and whether to log each request
;url = https://stats.allstarlink.org
;rate = 20
;log = false

[updates]
; base URL of the apt repository; <url>/latest.json = {"version","notes","date"}
; (empty disables release notes)
; optional override; the built-in default is https://w1jar.github.io/nodespot
;url = https://w1jar.github.io/nodespot
```

### Helper protocol (internal)

`nodespot-helper` listens on the socket (mode 0660, group `nodespot`) and also checks the peer uid (`SO_PEERCRED`: root or user `nodespot`). One JSON request per connection, one JSON response; operations are fixed and take no arguments: `update-status`, `update-check`, `update-apply` (`update-status` runs only `dpkg-query` and `systemctl show`, and reads the journal only while the job runs or once per finished job; `update-check` is kept for older servers and is the same as `update-status`, it no longer runs apt; `candidate` and `upgradable` are always empty/false; reply: `{"installed","candidate","upgradable","job","jobLog"}`) and `firewall-status`, `firewall-open` (reply: `{"present","running","serviceEnabled","port","portOpen","open"}`). The firewall operations take no arguments either: the helper reads the web port itself from `[server] listen` in `/etc/nodespot/nodespot.conf` (an invalid port is an error and nothing is changed). `firewall-status` queries the runtime and then the permanent configuration: `serviceEnabled` is the packaged `nodespot` firewalld service (TCP 8080, queried only when the port is 8080), `portOpen` is `<port>/tcp`, and `open` is true if either is. `firewall-open` adds the service for 8080 and `--add-port=<port>/tcp` for any other port, permanently, and reloads. Failures reply `{"error": "..."}`.

Configuration operations (`config-list`, `config-read`, `config-apply`, `config-log`, `config-show`, `config-restore`) carry the arguments in a `config` object; any other operation with a `config` field is rejected. Requests may be up to 4 MiB (others 4 KiB). The helper validates every path itself, runs `git` with a fixed argv and hardening options (history in `/var/lib/nodespot-config-history`, root only), preserves owner/mode, writes atomically and rolls back on error. `config-apply` fails with `conflict: <files>` when a `base` hash no longer matches. `asterisk-restart` runs `systemctl restart asterisk`.

System update operations (fixed apt/systemd commands, no arguments and no package names from the web process): `os-check` (`apt-get -q update`, then `apt-get -q -s upgrade`, both `LC_ALL=C`, noninteractive; reply `OSStatus`: `packages`, `keptBack`, `rebootRequired`, `rebootPkgs`, `job`, `jobLog`, and `warnings` when only the md380-emu repository failed to refresh; a held lock answers `apt is busy`), `os-status` (no apt), `os-apply` (transient unit `nodespot-osupgrade`), `reboot` (`systemctl --no-block reboot`) and `nodespot-restart` (`systemctl --no-block restart nodespot.service`). `os-check`, `os-apply`, `reboot`, the nodespot update and the channel switch are refused while an apt job runs.

System tab operations (typed, validated arguments in the request's `sys` object; always an argv vector, never a shell): `host-status` (`hostnamectl --json=short`, with a fallback to the kernel hostname, `/etc/hostname` and `/etc/os-release`; `timedatectl show`), `host-set-hostname` (`hostnamectl set-hostname <label>`), `host-set-timezone` (checked against `timedatectl list-timezones`, then `timedatectl set-timezone <zone>`), `host-set-ntp` (`timedatectl set-ntp true|false`), `host-timezones`, `host-poweroff` (`systemctl --no-block poweroff`), `logs-read` (`journalctl -u <allowlisted unit> -n <50..1000> --no-pager -o short-iso`, optional `--since`/`-b`), `services-status` (`systemctl show` for the fixed unit list) and `net-status` (`ip -j addr`, `ip -j route`, `ip -j -6 route`, DNS from `/etc/resolv.conf` or `resolvectl status`). WiFi (1.3.4, `wifi` request object, NetworkManager through `nmcli`): `wifi-status`, `wifi-scan`, `wifi-list`, `wifi-connect` (the password travels in the request, is written to a 0600 keyfile and never appears in argv or logs), `wifi-forget`, `wifi-priority` and `wifi-hotspot-set`. The helper binary also has a root-only subcommand, `nodespot-helper wifi-watchdog`, run by `nodespot-wifi-watchdog.timer` every 30 seconds; it starts and stops the setup hotspot and needs no running web server. Each one is one Go file per feature (`internal/helper/sys_*.go`) registered with `registerSysOp`.

ASL services and web port (`sys_svc.go`, `sys_webport.go`): `svc-status` (`systemctl show` for the fixed units, `ss -ltnH`, `firewall-cmd`), `svc-set` (`sys.svcScope` in `cockpit|apache|allmon3`, `sys.svcAction` in `enable|disable`, `sys.svcFirewall`; `systemctl disable --now` / `enable --now` / `stop` on the units of `svcScopes`; the units turned off are recorded in `/var/lib/nodespot/disabled-units`, one per line; errors `nodespot is using port 80`, `the service is not installed`), `web-port-status` and `web-port-set` (`sys.webPortAction` in `move|back|confirm`; state in `/var/lib/nodespot-webport`, the drop-in in `/etc/systemd/system/nodespot.service.d/port80.conf`; errors `port 80 is in use by another program`, `cannot check port 80 (ss is not available)`, `nodespot already uses port 80`, `nodespot is not on port 80`). The revert timer is a transient unit `nodespot-webport-guard` (`systemd-run --on-active=60s`) that runs `nodespot-helper web-port-guard` (root only, not a socket op): it moves nodespot back unless the move was confirmed. `postrm.sh` re-enables only the units in the record on `apt purge` (lines not on the allowlist are ignored) and removes `port80.conf`.

Diagnostics bundle (`diag-collect`, streaming, `internal/helper/sys_diag.go`): the request carries a `diag` object (`maskIdentity`, `version`, `report` and `audit` text built by the web process, `secrets` it knows, `callsigns` of the local nodes; any other op with a `diag` field is rejected). The request may be up to 4 MiB, like the config ops. The helper reads the config files itself (top-level `/etc/asterisk` files, `nodespot.conf`, Allmon3 `*.ini`, the digital modes files of the backup; regular files only, never a symlink, no key, certificate, database or session file), collects versions (`asterisk -V`, `uname -srm`, `dpkg-query -W`), system (`uptime`, `free -m`, `df -hP`), services (`systemctl show`, `ss -tulnH`), the network summary and the journal of the allowlisted units, redacts every file (secret keys, `register =>` passwords, PEM blocks, secrets found in the configs removed as literals everywhere, own identity when asked), builds the tarball in a private directory under `/run` or `/var/tmp`, and answers with a gzip stream, or one `{"error"}` JSON line before it (`a diagnostics bundle is already being built` maps to `diag_busy`). The temp directory is removed when the stream ends or the client disconnects.

Digital modes operations (`dv-status`, `dv-enable`, `dv-disable`, `dv-job`, `dv-config`, `dv-private`, `dv-restart`, `dv-restore-discard`; docs/DV.md): `dv-enable` runs the fixed script in the transient unit `nodespot-dv-enable` (apt install of the md380-emu vocoder, its qemu-user-static dependency, stop of the previous stack's units, start of `nodespot-dv`); `dv-job` reads its state and the last 200 journal lines; `dv-config` renders `/var/lib/nodespot-dv/config.json`; `dv-private` takes a `dv` request object (`get`, `set` or `clear` of the private node record) and is rejected on every other op; `dv-restart` restarts `nodespot-dv` when its control socket does not answer; `dv-restore-discard` deletes the settings a backup restore saved. The step helper accepts `dv-step` (and the older argv name for it).


Backup operations: `backup-info` (plain request/response: `{parts:[{id,present,size}],sa818}`), `backup-create` and `backup-restore` (streaming). `backup-create` takes `backup:{history,allmon3,sounds,dv}` and answers with one JSON line, the header (`arch`, `asl3Version`, `asteriskVersion`, `osId`, `osCodename`, `osName`, `channel`, `nodes`, `packages`, `parts`, `missingStock`, `skipped`, `entries` with per-file type, size and sha256), followed by a plain tar stream of the system files and EOF; an error before the header is an `{"error"}` line, a later failure cuts the stream short. `backup-restore` takes `restore:{dryRun,deleteMissing}` and is followed on the same connection by the gzip archive; the client half-closes after the last byte and reads one JSON result (`ok`, `files`, `steps`, node versions and Debian release, `sa818Port`/`sa818PortPresent`, `dvLegacySettings`). The helper re-validates the archive as root, classifies conffiles with `dpkg-query` (never a shell), writes to fixed destinations only (including `/etc/nodespot/nodespot.conf`, since the server cannot write there), and undoes config changes if a step fails. Streaming requests may take up to 20 minutes; the helper stays alive while one runs.

Terminal operations (`terminal.open`, `terminal.attach`, `terminal.list`, `terminal.end`; a typed `terminal: {rows?, cols?, owner?, id?, ack?}` object, rejected on every other op; sizes 1..500, default 24x80; nothing else is accepted, so no client string reaches the command line or the environment). `terminal.open` generates a 16 hex id, runs `systemd-run --unit=nodespot-term-<id> --service-type=exec --collect -- nodespot-helper term-host ...` (the session unit; `term-host` owns the PTY and runs `/bin/login -p`, see docs/DESIGN.md), waits for its root-only socket `/run/nodespot/term/<id>.sock`, and answers `{"ok":true,"id":...}`; `terminal.attach` does the same for an existing detached session (the owner must match). After the reply the same connection becomes a raw framed stream: type byte, 2-byte big-endian length, payload. Type 0 is data (both directions), 1 resize (client to helper), 2 the Linux user name (helper to client), 3 end (client to helper: end the session for good; helper to client: the session is over). Closing the connection without an end frame only detaches the session. `terminal.end` runs `systemctl stop nodespot-term-<id>`; `terminal.list` returns the live sessions and the unacknowledged ended records. When the helper starts it adopts sessions whose socket answers and stops units without one. The unprivileged web server only relays these frames to and from the WebSocket; it never sees a password prompt as anything but bytes.
