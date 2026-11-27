# nodespot 1.3.0 plan: System tab

Note: 1.3.0 to 1.3.4 ship together as 1.3.4. The per-version wording below is kept as written during development.

Status: **draft**. Sections 4 and 5 are built in 1.3.0; section 9 (WiFi manager, hotspot, watchdog) is built in 1.3.2 on `try/1.3.2` and not yet tested on a node; nothing else here is built. Items marked **to verify** came from search snippets or memory and must be checked on a real ASL3 node.

This plan supersedes `docs/PORT80.md`. Its useful parts are folded in below (sections 6 and 7).

## 1. Goal

ASL3 nodes are managed today with several web apps: nodespot, Cockpit, Apache with the landing page, and Allmon3. 1.3.0 adds one admin-only **System** tab so the common machine tasks live in nodespot, and an operator can turn off the apps they no longer need.

Everything in this plan is admin only. No existing role changes.

## 2. Decisions already made

These are decided by the owner. Section 15 records the review answers.

- New admin-only **System** tab at `#/system`, with sub-sections.
- **Updates moves into System.** `#/updates` stays as a redirect. `/api/updates*` is unchanged.
- **Terminal** opens a real Linux login prompt. nodespot never hands out a root shell itself. Admin only, never for operators. Audited, off by default until an admin enables it in Settings, with a 15 minute idle timeout.
- **WiFi manager** through NetworkManager (`nmcli`) behind fixed helper ops. Fallback is both: ordered saved networks and an open setup hotspot `nodespot-<hostname>` (section 9.3).
- **ASL services switch** for Cockpit, Apache and Allmon3: disable and enable only, never uninstall.
- **Move nodespot to port 80**, offered only when port 80 is free. Option A of PORT80.md. No redirect listener is kept on the old port.
- **Dependencies approved:** `github.com/coder/websocket` and xterm.js (lazy-loaded chunk).
- **Purge re-enables** only the units nodespot itself disabled (section 6.5).
- **Releases:** 1.3.x increments, one per branch group, each tested on a node before the next (section 13).
- **Extras, all chosen:** hostname and time, logs and services, network status, audit log viewer, diagnostics bundle.

## 3. What ASL3's tools offer

What each tool does, and where nodespot stands. "nodespot today" was checked against `web/src/components` and the routes in `internal/api/server.go`.

| Tool | Feature | nodespot status | Notes |
|---|---|---|---|
| Cockpit | System overview (CPU, memory, hostname, time) | 1.3.0 | Dashboard already shows uptime, load, temperature. Hostname and time are new. |
| Cockpit | Terminal | 1.3.0 | Login prompt only (section 8). |
| Cockpit | Logs (journal) | 1.3.0 | Allowlisted units, read-only. |
| Cockpit | Services (systemd) | 1.3.0 | Status list only. No start/stop of arbitrary units. |
| Cockpit | `networkmanager` module | 1.3.0 | Read-only network status, then WiFi. Static IP editing later. |
| Cockpit | `packagekit` (software updates) | Today | `Updates.svelte`: nodespot updates and OS (apt) updates, reboot-required. |
| Cockpit | `storaged` (disks, mounts, RAID) | Not planned | High risk, rarely needed on a Pi. Use Cockpit or SSH. |
| Cockpit | `sosreport` | 1.3.0 | Covered by the diagnostics bundle (section 12), not the same format. |
| Cockpit | AllStarLink `cockpit-wifimanager` | 1.3.0 | Scan, connect, forget, priority. Same priority property. |
| Apache landing page | Links and node info | Not planned | The optional public status page covers part of it. |
| Allmon3 | Node status, keyed/TX state | Today | Dashboard, live over SSE. |
| Allmon3 | Several nodes in one view | Later | nodespot shows the local node and its links. Multi-node view not built. Parked, see `docs/MULTINODE.md`. |
| Allmon3 | Connect, disconnect | Today | `ConnectControls`, `NodeConnectDialog`, operator role. |
| Allmon3 | Voter display | Later | Not built. |
| Allmon3 | Users in `/etc/allmon3/users` | Not planned | nodespot has its own users and roles. Allmon3 users are untouched. |
| `asl-menu` | Node setup | Today | `Setup.svelte` first-run wizard and the Config guided actions. |
| `asl-menu` | Backup and restore | Today | `Backup.svelte`, `RestoreFlow.svelte`; see `docs/BACKUP.md`. |

To verify on a real node, from search snippets only:

- ASL3 documentation's WiFi and timezone instructions (we assume NetworkManager and `timedatectl`).
- The exact module list in the Cockpit install on the Pi image.

**Verified** on a real ASL3 node (`apt-cache depends allmon3` and Allmon3's config files): allmon3 does not depend on `apache2`. It only Recommends `apache2` (or any httpd, such as nginx). Its hard dependencies are python3, python3-aiohttp, python3-aiohttp-session, python3-websockets, python3-argon2 and adduser. The daemon serves its own web UI on HTTP_PORT 16080 and websockets from WS_PORT_START 16700 upward (one per node), set in `/etc/allmon3/web.ini`, on all addresses by default. Apache only proxies `/allmon3/master/` to it (`/etc/apache2/conf-available/allmon3.conf`). Allmon3 talks to the Asterisk manager (AMI, port 5038) per `/etc/allmon3/allmon3.ini`.

## 4. System tab layout

Route `#/system`, sub-sections as a left list on desktop and a select on phones. A section is a hash suffix, for example `#/system/wifi`.

| Section | Source | Branch (release, see 13) |
|---|---|---|
| Overview | hostname, time, reboot, shutdown | 1 |
| Updates | existing `Updates.svelte`, moved | 1 |
| Network | read-only interfaces, IPs, gateway, DNS, link | 1 |
| Logs & services | journal viewer, service status list | 1 |
| Audit log | paged audit table | 1 |
| ASL services | Cockpit, Apache, Allmon3 switch | 2 |
| Web port | move nodespot to port 80 | 2 |
| WiFi | scan, saved networks, hotspot fallback | 3 |
| Terminal | login prompt in the browser | 4 |
| Diagnostics | bundle download | 5 |

Header changes:

- One **System** entry replaces **Updates** in the admin nav (`Header.svelte`). Admin only, like Updates today.
- The update count badge and the "N updates" or "Reboot needed" badge stay in the header and now link to `#/system/updates`.
- `#/updates` redirects to `#/system/updates` in `router.svelte.ts`, so old bookmarks and the badge links keep working.
- The reboot button in `Footer.svelte` moves to Overview. The footer keeps "restart nodespot" if it has one.

## 5. Hostname, time, logs, network status, audit

### 5.1 Hostname and time

- Hostname: `hostnamectl set-hostname`, validated as an RFC 1123 label, 1 to 63 characters. Shows a note that the `.local` name changes and that open sessions use the old address.
- Timezone: list from `timedatectl list-timezones`, set with `timedatectl set-timezone`. The value must be in that list.
- NTP: read `timedatectl show` (`NTPSynchronized`, `NTP`). Toggle `set-ntp`. Setting the clock by hand is not offered.
- Reboot and shutdown (new): `systemctl poweroff`. Both ask for confirmation and are audited. The existing `OpReboot` moves here unchanged.

### 5.2 Logs and services

Read-only. The helper runs `journalctl -u <unit> -n <lines> --no-pager -o short-iso` for units in a fixed allowlist:

`asterisk`, `nodespot`, `nodespot-helper`, `NetworkManager`, the previous digital voice stack's units (`mmdvm_bridge`, `analog_bridge`, `ysfgateway`, `p25gateway`, `nxdngateway`), and `nodespot-wifi-watchdog` once it exists.

Rules:

- Lines capped at 50 to 1000. Output capped in bytes. Optional `since` as a fixed choice (1 h, 24 h, boot), not free text.
- No free-form unit name, no grep argument. Filtering happens in the browser.
- A follow mode over SSE is **later**. First version is "refresh".
- The service list shows for each relevant unit: active state, enabled state, since. It uses the same allowlist plus `cockpit.socket`, `apache2`, `allmon3`, `firewalld`, `chrony` or `systemd-timesyncd` (**to verify** which one the image uses).

### 5.3 Network status

Read-only: interfaces, link state, addresses, default gateway, DNS servers. Source is `ip -j addr`, `ip -j route` and `resolvectl status` or `/etc/resolv.conf` (**to verify** on the image). Static IP editing is **later**.

### 5.4 Audit log viewer

The audit table exists (`store.AddAudit`, `store.ListAudit`) but `ListAudit` has no API. Add:

- `GET /api/audit?limit=&before=` returns entries newest first. Paging by `before` (entry id), not offset, so new entries do not shift pages. `ListAudit` gets a `beforeID` argument.
- The list shows time, user, action and the detail JSON, one line each. Filter by user and action prefix is done in SQL, not in the browser.
- `detail` is already specified as secret-free. The viewer does not change that rule.
- Read only. No delete or export in 1.3.0.

## 6. ASL services switch

### 6.1 What it does

Turn off, or back on, three ASL3 web services:

| Service | Unit | What listens | Notes |
|---|---|---|---|
| Cockpit | `cockpit.socket` | TCP 9090 | `cockpit.service` is static and starts only through the socket. Stop both when turning off. |
| Apache | `apache2` | TCP 80 and 443 | ASL3 landing page, and the `/allmon3/master/` proxy path. Allmon3 still answers on its own ports (see 6.4). |
| Allmon3 | `allmon3` | 16080 (web) and 16700+ (websockets), all addresses by default | Separate switch. The daemon serves its own web UI. |

Only `systemctl disable --now` and `enable --now`. Never `apt remove` or `purge`: the ASL3 Pi appliance meta-package (`asl3-pi-appliance`) depends on Cockpit and Allmon3, and removing them would remove the meta-package and leave the node different from the standard image. Disabled is the same for security and undoes in one click.

### 6.2 Helper op

A fixed unit allowlist, following the `dvScopes` pattern of the previous digital voice stack (`internal/helper/dv.go`):

```go
var svcScopes = map[string][]string{
    "cockpit":  {"cockpit.socket", "cockpit.service"},
    "apache":   {"apache2"},
    "allmon3":  {"allmon3"},
}
```

The request carries a scope name and an action (`enable` or `disable`). Both are checked against fixed sets. No unit name comes from the client.

### 6.3 Interface text

Top of the section: "nodespot covers much of what these provide in one place. Turn them off if you do not use them. You can turn them back on here at any time."

A caution box, shown before the confirm and listing what is lost per service:

- **Cockpit:** your fallback terminal, logs and WiFi settings if nodespot ever breaks. Make sure SSH works first.
- **Apache:** the ASL3 landing page, the `/allmon3/master/` proxy path, and anything else served on ports 80 and 443. Allmon3 itself stays reachable on 16080 and 16700+ (see 6.4).
- **Allmon3:** its monitoring daemon and web UI on 16080 and 16700+. Other people may use it, and may use it through a public link.

After a change the section shows, per service, the unit state (running, off) and the listening ports read from `ss -tlnH` (80 and 443 for Apache, 9090 for Cockpit, 16080 and 16700+ for Allmon3), so the operator sees that they are really closed. The change is audited.

### 6.4 Rules

- Never done during install or update. The installer and package scripts do not touch these units.
- Opt-in only, each service on its own switch, each change confirmed.
- Apache cannot be turned back on while nodespot holds port 80: see 7.4.
- Firewall: after Cockpit or Apache goes off, offer (not force) to close `cockpit` and `http`/`https` in firewalld. Turning a service back on offers to reopen. Uses the same mechanism as `firewall-open`.
- **Apache off does not close Allmon3.** The allmon3 daemon answers directly on 16080 and 16700+ on every address. So when Apache is turned off and Allmon3 is still on, the UI must say so: "Apache is off, but Allmon3 still answers directly on ports 16080 and 16700+ on all addresses." When the operator turns Apache off, offer to turn Allmon3 off too, as a separate checkbox, checked by default.
- The post-change port check (6.3) must include 16080 and 16700+ for Allmon3, not only 80 and 443.

### 6.5 Record of disabled units and purge

- Each `svc-set disable` appends the units it actually disabled (only those that were enabled before) to `/var/lib/nodespot/disabled-units`, one unit per line. `enable` removes the matching lines. The file is written by the helper op, root-owned.
- `apt purge nodespot` runs `packaging/scripts/postrm.sh`, which re-enables (`systemctl enable --now`) exactly the units in that file, then deletes the file. A unit nodespot did not disable, or that the operator disabled by hand, is left alone.
- `apt remove` (without purge) leaves all units as they are.
- If nodespot moved itself to port 80, purge also removes `/etc/systemd/system/nodespot.service.d/port80.conf` (nodespot's own file) and runs `daemon-reload`. Re-enabling Apache in purge happens after that, so port 80 is free.
- The script must never fail the purge: errors are logged and ignored. Tested with a fake `systemctl` in the script's unit tests.

## 7. Move nodespot to port 80

Option A of PORT80.md. Option B (Apache stays on 80 and proxies to nodespot) is **not chosen**: it ties nodespot to Apache's config, needs WebSocket proxy settings, and an ASL3 update to Apache can break it.

### 7.1 Why it fails today

1. Apache holds port 80.
2. `nodespot.service` runs with `CapabilityBoundingSet=` (empty), so the `nodespot` user cannot bind a port below 1024. The unit file documents a manual drop-in but nothing creates it.

### 7.2 When it is offered

Only when port 80 is free: no listener on `:80` in `ss`, meaning Apache is off. If Apache is running the section says so and points to the ASL services switch. nodespot never stops Apache as a side effect of this move.

### 7.3 Steps (helper op `web-port-80`)

1. Check port 80 is free (again, inside the helper).
2. Write a drop-in `/etc/systemd/system/nodespot.service.d/port80.conf`:

   ```
   [Service]
   AmbientCapabilities=CAP_NET_BIND_SERVICE
   CapabilityBoundingSet=CAP_NET_BIND_SERVICE
   ```

   The rest of the hardening stays (`NoNewPrivileges`, `ProtectSystem=strict` and the others). `NoNewPrivileges` does not block ambient capabilities set by systemd.
3. Change `listen` in `/etc/nodespot/nodespot.conf` to port 80, keeping the bind address if one is set.
4. `systemctl daemon-reload`, then restart `nodespot` with `--no-block`, as `nodespot-restart` does.
5. firewalld: open `http` (80/tcp) if firewalld is running. Keep the old port open until the operator confirms the new address works.

Going back is the reverse: remove the drop-in, set `listen` to 8080 (or the previous port, saved before the change), restart.

### 7.4 After the change

Same flow as the installer's `NODESPOT_PORT` and the existing restart wait (`awaitRestart`): the page shows "nodespot is moving to port 80. Open the new address", with a link built from the browser's current host, and keeps polling the old address for a failure. If the new port does not answer, the helper reverts after a timeout so the operator is not locked out (**proposed**: 60 s).

**Decided default:** no redirect listener is left on the old port. The move flow shows the new address and the installer-style "open the new address" step instead. Old bookmarks to 8080 stop working. This keeps a second listener closed. Adjustable later.

If an admin turns Apache back on while nodespot holds port 80, the services switch refuses and explains: "nodespot is using port 80. Move nodespot to another port first." A "move back to 8080" button sits next to the message. Apache would fail to start anyway, and a failed start leaves a confusing state.

### 7.5 PORT80.md

PORT80.md is superseded. Its by-hand commands (disable Cockpit, Apache, Allmon3; check ports; firewalld) stay useful for operators who prefer the shell: they move to `docs/HANDOVER.md` or a README section when branch 2 lands. PORT80.md is removed then.

## 8. Terminal

### 8.1 What the operator gets

A browser terminal showing a normal Linux `login:` prompt, like Cockpit's terminal or an SSH session. The operator logs in as a Linux user with that user's password. If that user can `sudo`, they can use it as usual.

nodespot never starts a root shell by itself, and being an nodespot admin does not give a Linux login.

### 8.2 Rules

- Admin only. Operators never get it (decided).
- **Off by default.** An admin turns it on in Settings. Turning it off ends open sessions.
- **Idle timeout** (decided default 15 minutes, adjustable later) closes the session. A hard maximum session length is also set (proposed 8 hours).
- Limit on concurrent sessions (proposed 2).
- **Audit each session:** start and end, nodespot user, Linux user if known, client IP, end reason (closed, idle, disabled, error). **Never keystrokes or output.** The Linux user is known only if it can be read from the login process (utmp or the child's session); if not it is recorded as unknown.
- No "remember me" and no password caching.

### 8.3 Why `login` keeps the privilege boundary

The helper is root, but it starts `login`, not a shell. `login` asks PAM to authenticate the Linux user, then drops to that user. nodespot sees only bytes: it relays what the browser sends and what the PTY returns. It does not parse or store the password, and the password is not an nodespot credential. If PAM rejects the login, the user gets the normal "Login incorrect", and PAM's own delays and lockout rules (for example `pam_faillock`, if configured) apply.

The root-privileged code is small: allocate a PTY, have systemd start `login -p` on its slave in a transient unit, apply the window size, relay bytes, stop the unit.

### 8.4 Where the PTY lives

Today the helper speaks one JSON request per connection over its unix socket, with fixed ops (`proto.go`). A new op fits that:

1. The web server connects to the helper socket and sends `{"op":"terminal.open"}` with typed size fields (rows and columns, bounded).
2. The helper checks the peer credentials as for every op (`peer_linux.go`), checks that the terminal feature is enabled (the helper reads the setting from the same config it already reads for the port), validates the request, generates the session id, starts the session unit with `systemd-run` (see below), waits for its socket, connects, and replies with one JSON line `{"ok":true}`.
3. The connection is then **upgraded to a raw byte stream**. After the reply the helper stops speaking JSON on it.
4. The web server relays bytes between this stream and the WebSocket.
5. **Resize** is a framed message. Frame format: a one byte type, a two byte length, then the payload. Type `0` is data, type `1` is resize (rows, cols as two uint16), type `2` is the Linux user name (for the audit record), type `3` is end (end the session for good). Both directions use the frame so a resize cannot be mistaken for data. The web server speaks frames on the helper side, and plain WebSocket binary and text messages on the browser side.
6. An explicit end (an end frame from the web server) makes the helper run `systemctl stop nodespot-term-<id>` (systemd ends every process of the unit). A connection that just closes only detaches the session (see below). The web server writes the audit entries (start, detach, reattach, end).

The helper keeps no session state of its own (decided in 1.3.3, second design): a session is a transient unit `nodespot-term-<id>` running `nodespot-helper term-host`, which owns the PTY, runs `login -p`, and listens on `/run/nodespot/term/<id>.sock` (root only). The helper only relays. If the helper or the web server restarts, relays drop and the session is **detached**, not ended.

**Own unit with term-host (decided).** `systemd-run --unit=nodespot-term-<id> --service-type=exec --collect -- nodespot-helper term-host --id .. --sock .. --rows .. --cols ..` (no sandbox, so the shell sees the real `/home` and `/tmp` and the helper keeps `PrivateTmp=yes` and `ProtectHome=yes`). Disconnect, idle, length, logout, switch off, the session limit and an administrator end a session (`systemctl stop` of the unit); a lost WebSocket, closing or reloading the page, a web restart or a helper restart detach it (a reload in the same tab reattaches by itself: the id is kept in sessionStorage). A detached session lives 2 minutes, and while `apt`, `apt-get`, `aptitude`, `dpkg` or `unattended-upgrade` runs in its cgroup (checked every 5 seconds, hard cap 2 hours). term-host keeps a 64 KiB ring buffer for the reattach replay. Reattach: the owner lists detached sessions in the Terminal section (Reattach, End) and uses the same ticket flow; detached sessions count towards the limit of 2; an administrator can end any. On start the helper adopts reachable sessions as detached and stops units with no reachable socket. The UI says: "If the page disconnects, the session keeps running for 2 minutes, and for as long as apt or dpkg is running in it, so an nodespot update typed here finishes safely. Reattach or just wait." Details: docs/DESIGN.md.

The new op follows the rule that no client string reaches a command line: `systemd-run` is started with an argument vector (no shell) built from a random hex id, the size and the owner id, and `login` gets a fixed argv and a clean environment (`TERM` from a fixed set, `LANG` fixed, no `PATH` from the client).

### 8.5 WebSocket endpoint

- Dependency: a small pure-Go library, `github.com/coder/websocket` (formerly `nhooyr.io/websocket`, approved by the owner). No cgo, `CGO_ENABLED=0` stays, armhf builds. Alternative: `gorilla/websocket` (also pure Go). The standard library has no server WebSocket.
- Route: `GET /api/system/terminal/ws?ticket=...`, admin.
- **Origin check.** The upgrade handler verifies `Origin` against the same allowed origins as the CSRF check (`auth.CheckCSRF`, `AllowedOrigins`). Cross-site WebSocket hijacking is the main risk, so a missing or wrong Origin is refused.
- **Session cookie.** The upgrade is authenticated with the normal session cookie (`SameSite`, `HttpOnly`) and the admin role is re-checked.
- **One-time ticket.** Browsers cannot add the `X-nodespot` CSRF header to a WebSocket handshake, so the header check cannot protect it. Instead the page first does `POST /api/system/terminal/ticket` (normal CSRF header, admin). The server returns a random ticket, valid for 30 seconds and usable once, tied to the session and user. The WebSocket URL carries the ticket. The server checks and deletes it on upgrade. Tickets are not logged and the query string is removed from access logs.
- The session is closed when the nodespot login session expires or the user logs out.
- Message size limits and a write timeout on the WebSocket. Idle timer is reset only by data from the browser.

### 8.6 Web

- xterm.js plus its fit addon, in a **lazy-loaded chunk**: loaded only when the Terminal section opens, so the main bundle does not grow. Dynamic `import()` in the Terminal component.
- Needs a CSP check: `connect-src` must allow the WebSocket to the same origin. The current policy has `default-src 'self'`, which covers same-origin `ws:`/`wss:` in current browsers but not all (**to verify** Safari). If not, add `connect-src 'self' ws: wss:` for same host only.
- The section shows the state (off, on, session open), an Enable link to Settings when off, and a note: "This is a Linux login. Use your Linux user and password, not your nodespot login."

## 9. WiFi manager

### 9.1 Scope

NetworkManager through `nmcli`, behind fixed helper ops with typed arguments. If NetworkManager is not present (`nmcli` missing or the service not running), the WiFi section says "NetworkManager is not available on this node" and does nothing: no buttons, no fallback to other tools.

### 9.2 Helper ops

| Op | Args (typed) | Does |
|---|---|---|
| `wifi-status` | none | NetworkManager present, WiFi device(s), radio on/off, active connection, Ethernet link |
| `wifi-scan` | none | `nmcli -t device wifi list --rescan yes`: SSID, signal, security, in-use |
| `wifi-list` | none | Saved WiFi connections with priority and autoconnect |
| `wifi-connect` | ssid, password (optional), hidden (bool) | Add or update a connection and bring it up |
| `wifi-forget` | connection id | `nmcli connection delete` for a WiFi connection only |
| `wifi-priority` | ordered list of connection ids | Writes `connection.autoconnect-priority` |

Details:

- **Typed args.** SSID: 1 to 32 bytes, no control characters. Password: 8 to 63 characters (WPA2/WPA3 passphrase) or empty for open networks. Connection id: must match an existing WiFi connection that the helper just listed. Nothing is passed through a shell, and values are passed as separate argv items, never joined into a string.
- **Password is write-only.** It is sent once, written to NetworkManager (which stores it root-only), and never returned by any op or logged by the helper or the web server. The audit entry records the SSID and the action, not the password. Same approach as the the previous digital voice stack password.
- **Priority.** Higher `autoconnect-priority` is tried first. Reordering writes descending values, for example 100, 90, 80. This is the same property ASL3's `cockpit-wifimanager` uses, so both tools agree (**to verify** on the image).
- The hotspot connection (9.3) is listed separately and is not editable here, not forgettable, and not reorderable.
- **Warn when changing the network you are connected through.** If the browser's address is on the WiFi interface (the web server compares the client's destination address with the interface addresses from `wifi-status`), connect, forget and reorder show: "You are using this network to reach nodespot. If the new settings fail, you may lose access." The confirm button says so. If no other path exists (no Ethernet), the warning is stronger and the hotspot fallback note is shown.

### 9.3 Fallback: ordered networks and a setup hotspot

Both, as decided.

1. **Ordered networks.** NetworkManager tries saved networks by priority. Nothing extra to build.
2. **Setup hotspot.** When no saved network connects within **3 minutes by default (configurable)** after boot or after losing the connection, and there is no Ethernet link, start a NetworkManager access point:
   - **Open network, no WiFi password.** SSID `nodespot-<hostname>`, for example `nodespot-node2001`. Protection is nodespot's own login, or the one-time setup code on a fresh node.
   - Address: a manual IPv4 address on a private subnet, for example `http://10.42.0.1:8080/` (**to verify** on the image), with DHCP and DNS for clients.
   - The hotspot stops as soon as a saved network or Ethernet connects, or when the operator turns it off.
   - It is not started when Ethernet has a link, or when the feature is off.
   - An admin can turn the fallback off entirely in the System tab (WiFi section). Off disables the watchdog's start action and removes the profile.

**Consequences and mitigations (open hotspot):**

- Anyone in radio range can join. They can reach only nodespot's login page (or setup page) and nothing else on the node.
- nodespot is plain HTTP, so a login typed over the open hotspot can be sniffed. The UI detects a client connected through the hotspot (the web server compares the client's destination address with the hotspot gateway address) and shows a warning bar: "You are connected over the open setup hotspot. Do not reuse an important password. If you logged in here, change your nodespot password once you are on a normal network." Setup over the hotspot should set a password that is not reused elsewhere.
- **No routing or NAT to other interfaces.** No uplink exists in that state, but it is configured so anyway: `ipv4.method manual` with an address (not `shared`, which turns on forwarding and masquerade) and `ipv4.never-default yes`, plus `net.ipv4.ip_forward` not touched by nodespot. Because `manual` has no DHCP server, the watchdog also needs a DHCP/DNS answer for clients: either `shared` with forwarding blocked by firewalld policy, or a small dnsmasq config. Exact nmcli settings are **to verify** on the image.
- **Firewall zone.** The hotspot interface goes in a dedicated firewalld zone (for example `nodespot-hotspot`, **to verify**) that allows only nodespot's port, DHCP and DNS, and has no forwarding or masquerade. If firewalld is not running, the plan states the hotspot is not offered (**to verify**; or it relies on the listen address).
- Short lifetime: the hotspot never stays up once a saved network or Ethernet connects.

### 9.4 Watchdog

A small systemd service plus timer, shipped in the helper package, **not** inside the web server, so it works when nodespot is down.

- `nodespot-wifi-watchdog.service` (oneshot) runs `nodespot-helper wifi-watchdog`, a subcommand of the helper binary, as root. `nodespot-wifi-watchdog.timer` runs it every 30 seconds after boot.
- It reads state with the same `nmcli` calls as `wifi-status`, keeps its "no link since" timestamp in `/run/nodespot/wifi-watchdog.json`, and starts or stops the hotspot connection.
- It does nothing if NetworkManager is absent, if WiFi is not in use, or if the feature is off. The feature is on by default; an admin can turn it off in the WiFi section, as decided in section 15.
- It logs to the journal (`nodespot-wifi-watchdog`) so the Logs section can show it.
- The hotspot connection profile is created by the helper when the feature is turned on, and removed when it is turned off.

## 10. Helper ops and API endpoints (proposed names)

All endpoints are admin only and follow the existing rules: JSON, CSRF header on writes, audited writes.

### 10.1 Helper ops

| Op | Branch | Notes |
|---|---|---|
| `host-status`, `host-set-hostname`, `host-set-timezone`, `host-set-ntp`, `host-poweroff` | 1 | `hostnamectl`, `timedatectl`. `reboot` exists. |
| `logs-read` | 1 | Unit from a fixed allowlist, lines, since |
| `services-status` | 1 | Fixed unit list |
| `net-status` | 1 | Read-only |
| `svc-status`, `svc-set` | 2 | `svcScopes`; enable/disable |
| `web-port-status`, `web-port-set` | 2 | Port 80 move and revert |
| `wifi-status`, `wifi-scan`, `wifi-list`, `wifi-connect`, `wifi-forget`, `wifi-priority`, `wifi-hotspot-set` | 3 | Section 9 |
| `terminal.open`, `terminal.attach` | 4 | Stream upgrade (section 8.4) |
| `terminal.list`, `terminal.end` | 4 | Plain request and reply |
| `diag-collect` | 5 | Section 12 |

### 10.2 API endpoints

| Method and path | Branch |
|---|---|
| `GET /api/system/host`, `PUT /api/system/host` | 1 |
| `GET /api/system/timezones` | 1 |
| `POST /api/system/poweroff` (`reboot` exists) | 1 |
| `GET /api/system/logs?unit=&lines=&since=` | 1 |
| `GET /api/system/services` | 1 |
| `GET /api/system/network` | 1 |
| `GET /api/audit?limit=&before=` | 1 |
| `GET /api/system/asl-services`, `PUT /api/system/asl-services/{cockpit,apache,allmon3}` | 2 |
| `GET /api/system/web-port`, `PUT /api/system/web-port` | 2 |
| `GET /api/system/wifi`, `POST /api/system/wifi/scan`, `POST /api/system/wifi/connect`, `DELETE /api/system/wifi/{id}`, `PUT /api/system/wifi/order`, `PUT /api/system/wifi/hotspot` | 3 |
| `GET /api/system/terminal`, `PUT /api/system/terminal`, `POST /api/system/terminal/ticket`, `GET /api/system/terminal/ws` | 4 |
| `GET /api/system/diagnostics/bundle` | 5 |

Existing `GET /api/system` (viewer, dashboard data) and `POST /api/system/restart-*` do not change.

## 11. Roles

Every new endpoint is `store.RoleAdmin`, through `authed`. Operators and viewers see no System tab, and a direct request returns 403. The SSE stream stays as is; only admin-only events are added if a section needs live data (for example "web port moving"), using the existing admin-only message variant in `sse.go`.

## 12. Diagnostics bundle

There is already a text report: `GET /api/diagnostics` builds `diag.Report` and masks secrets with `diag.RedactConfigLine` and the known-secrets list. The bundle extends that, with the same redaction code. It does not reuse the backup's approach: `docs/BACKUP.md` and `backup_dv.go` say the backup redacts nothing, because a backup must restore.

A tarball (`.tar.gz`) with:

- `report.txt`: the existing diagnostics report.
- `versions.txt`: nodespot, Asterisk, OS release, kernel, package versions of `asl3*`, `asterisk*`, the previous digital voice stack's packages.
- `services.txt`: the state of the services list (5.2) and listening ports.
- `journal/<unit>.txt`: the last 500 lines for each allowlisted unit.
- `network.txt`: network status (5.3), without WiFi passwords.
- `config/`: `/etc/asterisk` files and `nodespot.conf` with every secret value replaced by `REDACTED` (passwords, secrets, tokens, `manager.conf` secrets, IAX `secret`, registration passwords).
- `audit.txt`: last 200 audit entries.

Rules:

- Built by the helper (journal and config reads need root), streamed back, admin only, audited.
- No WiFi password, hotspot password, SQLite files, session data or terminal data ever goes in.
- Redaction is tested with a fixture of real-looking configs and a grep for the planted secrets in the output.
- The download page says what is excluded and tells the operator to read the bundle before posting it publicly.

## 13. Work plan

Each step is its own `try/*` branch and its own 1.3.x release. Each release is tested on a real node before the next branch is started.

| # | Branch (proposed) | Contents |
|---|---|---|
| 1 | `try/system-tab` | **1.3.0** System tab shell, Updates move and redirect, hostname and time, logs and services, network status, audit viewer |
| 2 | `try/system-services-port80` | **1.3.1** ASL services switch, move to port 80, purge record and `postrm.sh`. Status: built on `try/1.3.1`, not yet run on a real node. |
| 3 | `try/1.3.2` | **1.3.2** WiFi manager, hotspot fallback, watchdog. Built, not yet tested on a node. |
| 4 | `try/system-terminal` | **1.3.3** Terminal (WebSocket, xterm.js, helper PTY) |
| 5 | `try/system-diagnostics` | **1.3.4** Diagnostics bundle (built on `try/1.3.4`; not yet tested on a node) |

Status of step 4: built on `try/1.3.3` (version 1.3.3), not yet tested on a real node. `login` PAM behavior, the user lookup for the audit record and Safari WebSocket under the CSP are in "Still open".

Order reason: 1 is low risk and gives the shell the others plug into. 2 gives operators the main reason to turn Cockpit off, so 3 and 4 should land before people are told they can: WiFi and Terminal replace Cockpit's two fallbacks. Until 1.3.3 ships, the 1.3.1 caution box tells operators to keep Cockpit if they rely on its terminal or WiFi.

### Docs to update

- `docs/API.md`: every endpoint in 10.2, the WebSocket ticket flow, `GET /api/audit`.
- `docs/DESIGN.md`: section 3.4 table (new helper ops, the PTY upgrade, `login` as the boundary), section 3.1 (WebSocket added next to SSE), section 3.3 (WebSocket CSRF), "Default port 8080" note (port 80 option).
- `README.md`: System tab, port 80, services switch.
- `packaging/scripts/postrm.sh`: purge re-enables units from `/var/lib/nodespot/disabled-units` and removes the port 80 drop-in (6.5).
- `docs/PORT80.md`: removed with branch 2. `docs/HANDOVER.md` gets the by-hand commands if it is the right place.
- `packaging/systemd/nodespot.service`: replace the manual port comment with a pointer to the System tab. `packaging/` adds the watchdog units.

## 14. Test plan

Go unit tests (no root, no NetworkManager needed):

- Parsers with fixtures in `internal/helper/testdata`: `nmcli -t` output for scan and saved connections (hidden SSIDs, SSIDs with `:` and escaped characters, empty fields), `timedatectl show`, `timedatectl list-timezones`, `hostnamectl`, `ip -j addr`, `ip -j route`, `ss -tlnH`, `systemctl show` for the unit list.
- Validators: SSID, WiFi password, hostname, timezone, unit scope and action, log lines.
- Helper ops with the existing fake `Runner`: assert the exact argv for each op, and that no client string reaches argv outside typed positions.
- Purge: `postrm.sh` with a fake `systemctl`: re-enables only recorded units, `remove` does nothing, missing file is fine, drop-in removed.
- Services switch: Apache refusal while nodespot holds port 80; port-80 revert after timeout.
- Terminal: frame encoding and resize framing, ticket one-time and expiry, Origin rejection, cookie and role checks, idle timeout with a fake clock, audit entries contain no data bytes.
- Audit endpoint: paging with `before`, role check.
- Diagnostics: planted secrets absent from the whole tarball.
- Watchdog decision logic as a pure function of (WiFi state, Ethernet link, time since link lost, saved networks).

Web:

- There is no mock-data layer in `web/src` today (only `*.test.ts` unit tests). Add fixtures for the System API responses and run the sections against them in vitest, and in the Vite dev server through a small fake API, so the UI can be built without a node.

Manual, on a Pi with WiFi (and one with only Ethernet, and one without NetworkManager):

- WiFi: scan, connect, wrong password, forget, reorder, change the network you are connected through, hotspot start after the timeout, hotspot stops when a saved network returns, hotspot never starts with Ethernet plugged in, a hotspot client cannot reach other interfaces or ports besides nodespot, the hotspot warning shows, the System tab switch turns the fallback off.
- Services: disable and enable each, check ports, check `asl3-pi-appliance` is still installed, check an `apt upgrade` leaves them disabled.
- Port 80: with Apache off, move and move back; with Apache on, the offer is absent; re-enable Apache while on 80 is refused.
- Terminal: login as a normal user, wrong password, resize, idle timeout, disable while open, two browsers, an `Origin` from another site.
- Armhf: build and start the binary on a 32-bit image if a tester is available.

## 15. Decided in review

1. **Hotspot:** open network, SSID `nodespot-<hostname>`, protected by nodespot login or the first-run setup code. Consequences and mitigations in 9.3. Start delay 3 minutes by default, configurable. Admin can turn the fallback off in the System tab.
2. **Dependencies:** `github.com/coder/websocket` and xterm.js (lazy-loaded chunk) approved.
3. **Purge:** `apt purge nodespot` re-enables only units nodespot disabled, from `/var/lib/nodespot/disabled-units`; also removes the port 80 drop-in. `apt remove` changes nothing (6.5).
4. **Releases:** 1.3.x increments, 1.3.0 to 1.3.4, each tested on a node first (section 13).
5. **Defaults, not overridden (adjustable later):** terminal idle timeout 15 minutes; terminal admin only, never operators; no redirect listener on the old port after the move to port 80.

### Still open

From 1.3.1 (built, never run on a real node), to verify on a Pi with the ASL3 image:

- `systemctl show` on the real image: is `cockpit.service` reported `static`, is `cockpit.socket` `enabled`, and does `disable --now cockpit.socket` leave nothing listening on 9090.
- `apache2` on the image is the unit `apache2.service` and `disable --now` closes 80 and 443; `allmon3` closes 16080 and 16700+ (`ss -ltnH` output shape, IPv6 `[::]` and `*` columns).
- firewalld service names `cockpit`, `http` and `https` exist in the node's zone, and the permanent list query matches what a `--reload` makes live.
- Port 80 move: `AmbientCapabilities` in the drop-in with `NoNewPrivileges=yes` really lets the `nodespot` user bind 80; `systemd-run --on-active` as a transient timer survives `nodespot.service` and the helper restarting; the 60 s revert gives enough time on a Pi 3 (restart plus page load).
- The browser keeps its session across ports (cookies are not port-scoped) so the new page can confirm the move; the page's CSP forbids probing the new origin, so the UI only watches the old address come back.
- `apt purge nodespot` on a node where nodespot turned off Apache, Cockpit and Allmon3: all three come back and the drop-in is gone.
- An `apt upgrade` of ASL3 packages leaves a disabled service disabled.

Other **to verify** items, which stay where they are: WiFi and timezone tooling on the image, Cockpit module list, `cockpit-wifimanager` priority property, NetworkManager hotspot settings and firewalld zone (9.3), hotspot subnet, `chrony` or `systemd-timesyncd`, `resolvectl`, Safari WebSocket under the CSP.


Added by 1.3.2 (built, to verify on a node with WiFi):

- `nmcli -t` field order and escaping on the image, and the scan flags (`device wifi list --rescan yes`).
- Writing the keyfile (0600) into the NetworkManager directory and `nmcli connection load`, then `connection up uuid`.
- Hotspot profile settings (`802-11-wireless.band bg`, `ipv4.method shared` with the fixed address `10.42.0.1/24`, `never-default`) and that the Pi radio supports AP mode.
- firewalld zone `nodespot-hotspot` commands (target DROP, `--remove-forward`, allowed port, dhcp, dns) and that a hotspot client reaches only nodespot. Marked "to verify" in the UI and in DESIGN.md.
- That `shared` mode does not forward or NAT anything usable, since the hotspot is up only when there is no uplink.
- Client counting with `ip neigh`, and the radio hand-back (`nmcli device connect`) when a hotspot with no clients retries saved networks every 10 minutes.
- Watchdog timer timing (60 s after boot, every 30 s; hotspot opens about 3 minutes after boot).
- Detection of "connected through the hotspot" by the request's local address `10.42.0.1`.
- Default on versus the plan's section 9.4 wording (default off); 1.3.2 follows section 15 (on).

Terminal (1.3.3), on a real node: `login -p` on the image (PAM stack, `pam_securetty` or a `/etc/securetty` that would refuse root, lastlog and motd output, `login` timeout of 60 seconds at the prompt); that `login -p` keeps the fixed `TERM` and `LANG` for the shell; the Linux user lookup for the audit record (a non-root process in the unit's cgroup `/sys/fs/cgroup/system.slice/nodespot-term-<id>.service/cgroup.procs`, falling back to `/var/run/utmp`; some images may not write utmp for `login` on a pts); `/dev/ptmx` and `devpts` available to `nodespot-helper` under its unit hardening (`PrivateDevices`, `DevicePolicy`, `NoNewPrivileges` would break `login`; `PrivateTmp=yes` and `ProtectHome=yes` are set again); killing of the whole session on disconnect (`systemctl stop` of the unit with a background `sleep`, `su`, `tmux`); xterm.js rendering and paste on phones and Safari; idle and 8 hour limits; two browsers at once; an `Origin` from another site; behavior behind a reverse proxy that does not forward WebSocket upgrades.

Terminal sessions (1.3.3, term-host design), on the ASL3 image: `systemd-run --service-type=exec` of `nodespot-helper term-host` (the unit starts, the socket appears, `login -p` runs on the PTY with Setsid and Setctty and the shell gets it as controlling terminal); the utmp entry that `login` itself writes (`who`, `last`, and that two sessions do not collide); `login -p` under a transient unit (PAM, motd, lastlog, the 60 second login timeout, `TERM` and `LANG` kept); that the unit has the user's real `/home` and `/tmp`; that `systemctl stop nodespot-term-<id>` ends a session with a background `sleep`, `su` and `tmux` (tmux started from the terminal does not survive the stop: it is in the unit's cgroup); that no `nodespot-term-*` unit lingers after Disconnect; that `systemctl restart nodespot-helper` and `nodespot-web` leave the session running (detached, then reattach); that a session nobody reattaches ends after 2 minutes; that a detached session holding a running `apt` or `dpkg` waits and ends when it is done.

Terminal start (1.3.3 fix, on the ASL3 image): the first node run showed term-host ending the session within a second with no `login[...]` or `pam_` lines, which fits the `EIO` on the PTY master being taken for the end of the session. Now the end is the exit of `login`, and the journal says how (status or signal, time, first output). To confirm on the node that a login prompt appears and that a `vhangup()` by `login` no longer ends the session; if it still fails, the new log lines name the cause.

**Test `apt upgrade nodespot` from the terminal on a node** (not done yet): open the terminal, run `sudo apt upgrade` with a newer nodespot available, watch the page lose its connection when the postinst restarts the helper and the web server, then reconnect and reattach; check that dpkg finished and that the session ended by itself (`apt-finished`) or was ended with Disconnect. Also: `kill -9` of the helper while a session is open, a reboot with a detached session (units are transient, nothing remains), and 2 detached sessions blocking a third.

Diagnostics (1.3.4), to verify on a node: the `dpkg-query -W -f` output and the package patterns (`asl3*`, `asterisk*`, `allmon3`, the previous digital voice stack's packages, `nodespot*`); `ss -tulnH` and `nmcli device` output; which journal units exist; that `/run` is writable under the helper unit (`PrivateTmp`; `/var/tmp` is the fallback); the file paths of the the previous digital voice stack and Allmon3 configs; masking of a public IP that is only behind NAT (it is found from `ip -j addr` and `externip`/`externaddr` keys; with neither, the address is not known and stays); and browser handling of a large download (the UI saves it as a blob).

Examples in this document use placeholders: callsign N0CALL, node 2001, hostname `node2001`, address `192.0.2.10`.
