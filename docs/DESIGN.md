# nodespot Design Document

nodespot is a modern web control panel for AllStarLink 3 (ASL3) nodes. ASL3 is Asterisk (22 as of ASL3 3.10; earlier releases used 20 LTS) plus `app_rpt`, the amateur-radio linking system. This document describes goals, architecture, the configuration engine, safety model, packaging, and the v1 scope.

Items marked **To verify** are assumptions that need confirmation on real hardware or against upstream sources.

## 1. Goals and requirements

| Requirement | Meaning |
|---|---|
| No PHP | Single Go binary, no interpreter runtime on the node. |
| Full configurability | Every ASL3 file is reachable: forms for known settings, a raw editor for everything. |
| Modern look and feel | Responsive, mobile-first, dark and light themes. |
| Easy install and update | One-line install, signed apt repo, updates through `apt upgrade`. |
| Works with any configuration | Real nodes vary widely. The panel adapts to what is on disk; migration is optional. |

**Targets.** Raspberry Pi 3, 4, 5 and Zero 2 W on Debian 12 (bookworm) or 13 (trixie) and Raspberry Pi OS, arm64, plus amd64. 32-bit armhf (built with GOARM=6, so it runs on the Raspberry Pi Zero W and every other armhf board) is a hotspot and network node only: no software AMBE vocoder, no analog radio audio (the radio presets with audio show a warning). `install.sh` accepts armhf with a note and refuses armel (ARMv5).

**Prior art.** AllScan (PHP: connects and manages favorite nodes; the main inspiration for nodespot's dashboard) and Allmon3 (ASL's monitor, widely used for public status pages). nodespot covers AllScan's role, coexists with Allmon3 and other AMI clients, and goes much further on configuration.

## 2. Scope and non-goals

> nodespot replaced Asterisk and ASL3 with its own node; see [NATIVE_NODE.md](NATIVE_NODE.md). This document describes the aslcp 2.x design and is kept for reference.

In aslcp 2.x, the panel was installed after ASL3 was present. nodespot needs neither ASL3 nor Asterisk.

**Non-goals**

- Installing ASL3 or Asterisk itself.
- Non-Debian distributions (for now).
- Telephony features (SIP, PSTN, voicemail, queues). The stock telephony files are present on disk but not loaded; they are reachable only through the raw editor and hidden by default.

## 3. Architecture overview

```
Browser (Svelte SPA)
   |  JSON REST + Server-Sent Events
   v
nodespot (Go, user: nodespot) ---- AMI :5038 ----> Asterisk / app_rpt
   |        |
   |        +--> SQLite (/var/lib/nodespot)
   |
   +-- unix socket --> nodespot-helper (privileged, fixed allowlist)
                          |-- atomic writes under /etc/asterisk
                          |-- allowlisted `asterisk -rx` reloads
                          `-- systemctl restart asterisk / allmon3
```

### 3.1 Stack

| Layer | Choice | Notes |
|---|---|---|
| Backend | Go, single static binary | Frontend embedded via `go:embed`. Target ~15-30 MB RSS (**To verify** on a Pi Zero 2 W). Trivial cross-compilation. |
| Frontend | Svelte 5, Vite, TypeScript, Tailwind, shadcn-svelte | Mobile-first, theme switch. |
| API | JSON REST plus Server-Sent Events (SSE) for live updates, and one WebSocket (the admin terminal, `github.com/coder/websocket`) | Contract in `docs/API.md`. SSE is one-way, standard-library only, and proxy-friendly; commands go over REST. The WebSocket carries only the browser terminal (section 3.4). |
| Live data | AMI client in Go | Events bridged to SSE. |
| Storage | SQLite via a pure-Go driver (e.g. `modernc.org/sqlite`) | Users, favorites, audit log, settings, config snapshot metadata. |

### 3.2 Live data pipeline

Formats below were taken from the app_rpt source (`rpt_manager.c`, `rpt_link.c`, `rpt_cli.c`) and Allmon3's implementation. They still need confirming against a live node.

The Go AMI client connects to `localhost:5038` and maintains one authenticated session with reconnect, backoff and `Ping` keepalive. It combines two sources:

1. **Events (instant).** app_rpt emits AMI events named after its variables: `RPT_RXKEYED`, `RPT_TXKEYED`, `RPT_ETXKEYED`, `RPT_LINKS`, `RPT_ALINKS`, `RPT_NUMLINKS`, and others, each carrying `Node` and `EventValue`. They use the `call` event class, so the AMI user needs `read` including `call` (or `all`), and the login must request events. Allmon3 logs in with events off and relies only on polling.
2. **Polling (resync).** `Action: RptStatus` with `Command: XStat` (connections table, network-wide linked-node list with modes, keying variables, node state flags) and `Command: SawStat` (per-link keyed flag and seconds since last key/unkey). Fast polling (about 1 s) only while a viewer is watching, slow otherwise.

Link control uses `rpt cmd <node> ilink <code> <target>`: 1 disconnect, 2 monitor, 3 transceive, 8 local monitor, with permanent variants. Inputs are strictly validated before being placed in a command.

Allmon3 bugs we avoid: it looks for `RPT_TXEKEYED` instead of `RPT_ETXKEYED` (so it never shows external TX), and it ignores the `L` (local monitor) link mode.

Allow/deny lists live in AstDB and are managed through AMI database actions (UI in a later release).

### 3.3 Authentication and web security

- Local users, argon2id password hashes, server-side sessions with secure, HttpOnly, SameSite cookies.
- Roles: `admin` (everything), `operator` (connect/disconnect, favorites, monitoring), `viewer` (read-only, no secrets).
- CSRF protection on all state-changing requests (custom header plus Origin check).
- WebSocket (terminal): a browser cannot send the CSRF header on a handshake, so the page POSTs (session cookie plus the custom header) for a one-time ticket (30 seconds, single use, tied to the user and the login session) and passes it as `?ticket=`. The upgrade also requires the cookie, the admin role and an `Origin` that matches the host (a missing one is refused). The CSP names the host's `ws://` and `wss://` in `connect-src`.
- Login rate limiting.
- Optional HTTPS: a self-signed certificate generated at install, or a user-supplied certificate.
- Default port 8080, configurable, so nodespot coexists with Allmon3 and Cockpit. With Apache off, Settings, System can move nodespot to port 80 (a systemd drop-in with `CAP_NET_BIND_SERVICE`, a 60 s revert timer if the new address is not opened) and the ASL services switch can stop and disable Cockpit, Apache and Allmon3 (never uninstall).

### 3.4 Privilege separation

The web process runs as the dedicated `nodespot` user, never root. A small separate binary, `nodespot-helper`, performs the only privileged operations:

| Operation | Constraint |
|---|---|
| Write a file under `/etc/asterisk` | Atomic (temp file + rename in the same directory), preserving owner and mode. Path must resolve inside the allowed tree; no symlink escapes. |
| `asterisk -rx <cmd>` | Command must match a fixed allowlist (reload variants, status queries). No shell, no free-form arguments. |
| `systemctl restart` | Only `asterisk` and detected AMI-client services (e.g. `allmon3`). |
| `nmcli` WiFi operations (1.3.4) | Typed arguments only (SSID 1 to 32 bytes, PSK 8 to 63 ASCII or 64 hex, connection ids checked against a fresh list). The password is written to a 0600 NetworkManager keyfile, never put in argv, logs or the audit log. |
| `nodespot-helper wifi-watchdog` (1.3.4) | Root only, run by a systemd timer. Starts or stops the `nodespot-hotspot` connection. |
| `terminal.open`, `terminal.attach`, `terminal.list`, `terminal.end` | Start a session unit `nodespot-term-<id>` running `nodespot-helper term-host` (fixed argv; only the window size, the owner id and a generated hex id are variable, all validated), relay its socket, list sessions, or stop a unit. |

#### WiFi hotspot watchdog (1.3.4)

- **Why a separate watchdog.** The hotspot exists for the case where the node cannot reach any network, which includes a web server that has not started or has crashed. A goroutine inside `nodespot` would not run then. So the check is a oneshot `nodespot-wifi-watchdog.service` started every 30 seconds by a timer, running the helper binary's `wifi-watchdog` subcommand. The decision is a pure function (`WatchDecide`) of WiFi state, Ethernet link, time without a network and saved networks, and is unit tested. State is in `/run/nodespot/wifi-watchdog.json`; a file lock and an in-process mutex keep it from racing the web-driven ops.
- **Rules.** Start after 3 minutes (30 to 3600 s, adjustable) with no active connection and no Ethernet link; stop as soon as a saved network connects. One radio cannot be an access point and a client at once, so while the hotspot is up with no clients, the watchdog stops it every 10 minutes to try the saved networks again.
- **On by default.** The hotspot fallback is on by default, as decided in review (plan sections 9.4 and 15). An admin can turn it off, and the choice is stored in `/var/lib/nodespot-wifi/hotspot.json`.
- **Open network.** There is no WiFi password; protection is the nodespot login or the one-time setup code. Anyone in range can sniff the login, so the page warns while a visitor is connected through the hotspot (`GET /api/setup` returns `hotspot`).
- **No NAT, no forwarding.** The profile uses NetworkManager `ipv4.method shared` only to get a DHCP server and the fixed address `10.42.0.1`; `shared` normally means NAT, so this is limited three ways: the profile is `never-default`, the hotspot is up only when the node has no uplink (so there is nothing to forward to), and the watchdog stops it when a network connects. If firewalld is present, a zone `nodespot-hotspot` (target DROP, only the nodespot port, DHCP and DNS allowed, forwarding removed) is bound to the interface. **To verify** on a node: the exact firewalld commands, and that no forwarding or NAT rule remains. Without firewalld the hotspot is still offered and the page says it is not isolated.

**The browser terminal (1.3.4).** `login` is the security boundary: nothing starts a shell for the web user or for root, so a person still has to authenticate to PAM with a Linux account, and the shell runs as that account. A session is a transient systemd unit `nodespot-term-<id>` whose main process is `nodespot-helper term-host`. term-host owns the PTY master and runs `/bin/login -p` on the slave; the helper is only a relay between the web server and term-host's socket; the unprivileged web server relays bytes between the WebSocket and the helper.

**Why term-host.** In the first design the helper held the PTY master and `login` ran in its own unit. A restart of the helper (the package's postinst does one on every upgrade) closed the master, the kernel hung the terminal up, and an `apt upgrade` typed in the terminal was killed half way. Now the process that holds the master is inside the session's unit, so restarting the helper or the web server does not touch it.

    systemd-run --quiet --unit=nodespot-term-<16 hex> --description="nodespot web terminal" \
      --service-type=exec --collect --property=TimeoutStopSec=5s -- \
      /usr/libexec/nodespot/nodespot-helper term-host --id=<16 hex> \
      --sock=/run/nodespot/term/<id>.sock --rows=R --cols=C --owner=N

as an argument vector, no shell (`terminal_unit.go`, tested). The unit has no sandbox properties, so the shell is an ordinary login session outside the helper's cgroup and mount namespace (a user sees the real `/home` and `/tmp`; the helper keeps `PrivateTmp=yes` and `ProtectHome=yes`). No `ReadWritePaths` or `RuntimeDirectory` is needed: the helper has no `ProtectSystem`, and `/run/nodespot` exists from the helper socket (term-host creates `/run/nodespot/term` with mode 0700).

**term-host** (`termhost.go`) refuses to run unless uid is 0 and validates every argument (16 hex id, socket path exactly `/run/nodespot/term/<id>.sock`, rows and cols 1..500, owner a non-negative number; anything else is an error). It opens the PTY (`/dev/ptmx`, `TIOCSPTLCK`, `TIOCGPTN`, `TIOCSWINSZ`), starts `/bin/login -p` with `Setsid` and `Setctty` on the slave and a fixed environment (`TERM`, `LANG`, `PATH`), and listens on the socket (mode 0600, directory 0700, root only; only the helper connects). It serves at most one client at a time with the frame protocol: 1 type byte, 2 bytes big-endian length, payload. Types: 0 data, 1 resize (client to host; the first resize makes a connection an attach, so a connect-and-close is a harmless liveness probe), 2 Linux user name (host to client), 3 end (client to host: end the session for good). It always drains the PTY into a 64 KiB ring buffer, so a detached session never blocks and a reattach gets the recent output replayed first. A client that stalls for more than 5 seconds is detached.

**Detach and end.** A session ends for good only by an explicit act: the Disconnect button (a `{"type":"end"}` message), the idle timeout (15 minutes of keyboard input), the 8 hour maximum, the 2 session limit, a logout or loss of the admin role, turning the terminal off, or an administrator ending it. The web server then sends the end frame (or calls `terminal.end`), and the helper runs `systemctl stop nodespot-term-<id>` (systemd ends every process of the cgroup). Anything else is a transport loss and only **detaches**: the WebSocket closing without an end (closing or reloading the page too), a web server restart or shutdown, the helper restarting or exiting. A detached term-host keeps the shell running and ends the session itself after a **2 minute grace**, unless `apt`, `apt-get`, `aptitude`, `dpkg` or `unattended-upgrade` runs in the unit's cgroup; then it waits and checks every 5 seconds, with a hard cap of 2 hours from the detach (`graceMachine` in `termhost_state.go`, tested with a fake clock and a fake detector). So `apt upgrade nodespot` typed in the terminal finishes even though its postinst restarts the helper and the web server; the operator reattaches. A reload in the same tab reattaches by itself: the page keeps the session id in `sessionStorage` and, once the old page has detached, asks for a ticket for it. A helper that sees term-host close the connection tells its client with an end frame, which is how the web server tells "session over" from "transport lost" (a bare EOF is always a detach).

**How the end of a session is detected.** A session ends by itself only when its child, `login`, exits (term-host waits for the process, never for the PTY). Reading the PTY master is not an end signal: util-linux `login` calls `vhangup()` on its terminal and reopens it, and until then every read on the master returns `EIO`. term-host therefore keeps its own slave descriptor (opened with `O_NOCTTY`, so it is not term-host's controlling terminal) for as long as the child lives, treats a read error with a live child as a pause (it logs the first one, waits 50 ms and reads again), and after the child has exited closes the slave and drains the last output for at most 300 ms. The child is started with `Setsid` and `Setctty` and the slave as stdin, stdout and stderr (`Ctty` is 0, the child's descriptor number, not term-host's). The end reason `exited` always has a cause in the journal and in the `detail` of `<id>.ended.json`: `login exited with status 1 after 0.2s` or `login killed by signal hangup after 3s`. When the session ends within 10 seconds of its start, term-host also logs the first 2 KiB that login wrote to the PTY (printable ASCII only, newlines as `\n`), because an error such as a PAM failure is often printed only on the terminal. If the session ended before the helper could connect, the helper reads that record and answers `terminal not available: the session did not start (<detail>)`, which the web server passes to the page as a WebSocket close reason (`start: ...`) so the operator sees it. On a node: `journalctl --since '-10 min' -g 'nodespot|login|pam_'`.

**Reattach.** `GET /api/system/terminal/sessions` lists the live sessions (attached or detached); the owner reattaches a detached one through the same ticket flow (`{"session":"<id>"}`), the ticket carrying the target; ownership is checked by the web server and again by the helper (`terminal.attach` takes the owner id from the web server, the state file `/run/nodespot/term/<id>.json` names the owner). An administrator can end any session. Detached sessions count towards the 2 session limit.

**Helper start.** Before the helper accepts its first connection it lists `nodespot-term-*.service`. A unit whose socket answers is adopted as a detached session (nothing to do: term-host keeps its own state); a unit with no reachable socket is broken and is stopped. State files of units that are gone are removed. Ended sessions leave `<id>.ended.json` records (kept at most 24 hours until the web server acknowledges them through `terminal.list`), which the web server turns into audit entries for sessions that ended while nobody was attached (`grace`, `apt-finished`, `exited`).

**Audit.** `system.terminal.start`, `.detach`, `.reattach` and `.end` (`reason`: `closed`, `idle`, `limit`, `disabled`, `logout`, `grace`, `apt-finished`, `admin-ended`, `error`, `exited`), with user, Linux user, duration and id prefix. Keystrokes and output are never recorded; the ring buffer lives only in term-host's memory.

**utmp and `who`.** `login(1)` writes the utmp and wtmp records for its own line, so `who`, `w` and `last` show the session as for any login on a `pts` (the unit-level `TTYPath`, `UtmpIdentifier` and `UtmpMode` of the earlier design are gone). If a term-host dies without `login` cleaning up, the record is left stale until the next boot, as for any killed login. The Linux user for the audit record is the owner of the first non-root process in `/sys/fs/cgroup/system.slice/nodespot-term-<id>.service/cgroup.procs` (the v1 path is tried too), falling back to the utmp record of the line `pts/N`.

**Residual risks.** term-host runs as root and unsandboxed by design (it must hold the PTY and start `login`); its attack surface is its argument vector (validated, set only by the helper), its socket (root only, one frame parser) and nothing else. If the helper is compromised it already is root. A session left detached while `apt` hangs holds for 2 hours at most. Updating nodespot from the terminal is now supported; it is still to be tested on a node (PLAN-1.3.0 "Still open").

On other platforms the operation answers "terminal not available". `--demo` mode has a pretend login that never lets anyone in.



Invocation is via a systemd socket-activated service (preferred) or a polkit/sudoers rule with the same allowlist. The helper validates every request independently of the web process. The choice between the two mechanisms is a **To verify** item (see section 13).

## 4. Configuration engine

The panel must work with whatever is on disk. The engine has three layers.

### 4.1 Layer 1: syntax (`internal/astconf`)

A lossless parser and writer for Asterisk configuration. It builds a concrete syntax tree that retains every byte.

**Must handle**

- Comments (`;` and block comments), blank lines, alignment, trailing comments.
- Both `=` and `=>` operators.
- Repeated keys (e.g. several `allow =` lines): ordered and preserved.
- Template stanzas `[name](!)`, inheritance `[node](parent)`, multi-parent `[x](!,a,b)`, and non-template parents.
- `#include` and `#tryinclude` with quoted or unquoted paths, globs (expanded alphabetically), and trailing comments.
- Values containing `,` `|` `;` `"` or `${}`: treated as opaque strings, never split or re-quoted by the engine.

**Editing model.** Edits are expressed as operations on nodes (set value, insert key, delete key, add stanza). Serialization re-emits original bytes for untouched nodes, so only changed lines differ in the diff.

**Golden test.** Parse followed by serialize of an unmodified file must be byte-identical. This runs over the whole anonymized corpus in CI.

**Resolver.** Computes effective values with provenance: for each key, which file, stanza (template or node), and line it comes from. Last parent wins; the stanza's own keys override inherited ones. The resolver follows includes in order so the UI can say "this value comes from `custom/rpt/foo.conf:12` via template `node-main`".

### 4.2 Layer 2: schema (`internal/schema`)

A knowledge base of known settings per file and stanza.

| Field | Purpose |
|---|---|
| type | string, int, bool (`yes/no`, `1/0`), enum, node number, duration, list |
| allowed values / range | Drives validation |
| default | Shown as placeholder; "effective default" display |
| description | Inline help |
| reload requirement | `none`, module reload, dialplan reload, restart (**To verify** per key on hardware) |
| sensitive | Masked in UI, excluded from logs and diagnostics |

Rules:

- Known keys render as typed forms with validation.
- Unknown keys are always shown and editable as raw key/value pairs and are never dropped.
- Every file also has a raw text editor (CodeMirror with syntax highlighting). Save runs the validation pipeline first (parse check, schema warnings, cross-file checks).
- `extensions.conf` is treated as code: raw editor, plus a form for `[globals]` only.
- `modules.conf` is handled specially: `autoload=no`, `load`/`noload`/`require`, with mixed `=` and `=>` preserved.
- `;MENU:category:type:description` machine-readable comments in `custom/` files (used by `asl3-menu`) are preserved byte-for-byte and may later be used to label custom entries in the UI.

### 4.3 Layer 3: guided actions

Wizards for common multi-file operations: add node, change node number, change radio interface, set up EchoLink, registration.

Each action:

1. Checks preconditions against the parsed tree.
2. If the config does not match expectations, refuses with a clear explanation and a link to the raw editor, rather than guessing.
3. Produces a full multi-file diff for review before anything is applied.
4. Applies through the standard safety pipeline (section 6).

**Node renumbering** is the canonical example. A node number appears in roughly eight places:

| Location | Reference |
|---|---|
| `rpt.conf` | `[nodes]` entry and the node stanza name |
| `simpleusb.conf` / `usbradio.conf` | Device stanza |
| `voter.conf` | Voter stanza |
| `echolink.conf` | `astnode` |
| `extensions.conf` | `[globals]` `NODE` |
| `allmon.ini.php` | Node section of the legacy Allmon2/Supermon-format file (also read by AllScan), if present |
| `savenode.conf` | Shell `KEY=VALUE` file |
| `rxchannel` values | Channel references |

A rename is one guided transaction covering all of them, or none.

**Radio interface changes.** Changing `rxchannel` implies matching `modules.conf` load lines and a matching device stanza; the wizard handles all three together.

**Layout migration** (old flat layout to the current templated layout with `custom/` includes) is an optional, previewed, reversible tool planned for a later version. It is never required.

### 4.4 Files in scope

| Group | Files |
|---|---|
| Core | `rpt.conf` (stanzas such as `[general]`, `[nodes]`, `[node-main](!)`, per-node stanzas, `[functions]`, `[telemetry]`, `[morse]`, `[wait-times]`, `[memory]`, `[macro]`, `[events]`, `[controlstates]`, `[schedule]`) |
| Radio | `simpleusb.conf`, `usbradio.conf`, `res_usbradio.conf`, `voter.conf` |
| Linking | `iax.conf`, `echolink.conf`, `rpt_http_registrations.conf`, `rpt_auth.conf` |
| Platform | `manager.conf`, `modules.conf`, `extensions.conf`, `logger.conf`, `http.conf`, `gps.conf`, `asterisk.conf` |
| Custom | `custom/**` includes |
| Non-Asterisk | `savenode.conf`; AMI-client configs (see 4.5) |

### 4.5 Other AMI clients

Other tools may use the same AMI login: Allmon3, AllScan, Supermon. nodespot does not depend on any of them. It detects which are installed (packages, services, config files) and, when the AMI credentials in `manager.conf` change, either updates their configuration or shows exactly which tools will stop working until updated. Detection details are **To verify** (section 13).

## 5. Secrets handling

Secrets are spread across `manager.conf`, `iax.conf`, `echolink.conf`, `voter.conf`, `rpt_http_registrations.conf`, `savenode.conf`, `custom/iax/*`, and AMI-client configs such as `allmon.ini.php` and Allmon3's config.

- Masked in the UI by default; reveal requires `admin` and is audited.
- Never written to application logs or audit-log payloads.
- Excluded from exported diagnostics unless the user explicitly opts in.
- Config history stays local to the node (see section 6).
- Diff views mask sensitive values unless an admin reveals them.

## 6. Safety pipeline

Every apply follows the same sequence:

1. **Validate**: parse, schema and cross-file checks.
2. **Snapshot**: commit the current `/etc/asterisk` into a git repository at `/var/lib/nodespot-config-history`, with author and message. Secrets stay local; the repository is never pushed anywhere.
3. **Atomic write** through the helper.
4. **Targeted reload** using the per-key reload requirement; restart only when needed.
5. **Health check** via AMI: Asterisk responds, node is registered and up.
6. **Rollback offer**: if the health check fails, offer one-click automatic rollback to the snapshot, followed by the same reload and check.

Additional features:

- History view with per-commit diffs and one-click restore (itself a snapshot-protected apply).
- Audit log in SQLite: who, when, what changed (files and keys, secrets redacted).
- Multi-file applies are best-effort atomic: all files are staged, written in sequence, and any failure restores the snapshot. True filesystem-level atomicity across files is not available. **To verify** that this is sufficient in practice.

## 7. "Upstream changes" view

Package updates leave `*.dpkg-dist` files beside live ones, and live and dist can differ structurally. A naive text merge is not acceptable.

The view parses both files and compares per stanza and per key: added, removed, and changed keys, plus template/inheritance differences. The user adopts individual changes; each adoption goes through the normal safety pipeline. Planned for post-v1.

## 8. Setup wizard

The first-run wizard can replace the `asl3-menu` node setup.

| Step | Details |
|---|---|
| Detect | Is ASL3 present, which version, existing nodes, existing layout. |
| Node | Node number and password; registration entries. |
| Identity | Callsign, ID, basic `[general]` settings. |
| Radio interface | SimpleUSB, USBRadio, none (local/pseudo hub), USRP (digital modes bridge: see [DV.md](DV.md)), Voter. |
| AMI | Create or update credentials; update or warn about detected AMI clients (section 4.5). |
| Review | Full multi-file diff, then apply through the safety pipeline. |

On an already configured node, the wizard is not forced: it detects the existing setup, offers read-only import into the panel, and sends the user to the guided actions for changes.

## 9. Feature split

### 9.1 v1 (MVP)

v1 ships in two milestones. The first covers what AllScan does today (dashboard, connect/disconnect, favorites); configuration comes second.

**Milestone 1: dashboard and favorites**

- Auth (local users, roles).
- Live status of local nodes: connected-links table (node, description, direction, connected time, mode, keyed), node TX/RX keying indicators, last-heard per link, whole-network linked-node list.
- Connect, disconnect, monitor and local monitor (optionally permanent) from favorites or by node number; disconnect all.
- Favorites: add, edit, group, reorder; import from an existing AllScan installation.
- Node lookup: callsign, description and location from the ASL node list, shown next to every link and searchable.
- System strip: CPU temperature, load, uptime, Asterisk uptime.
- `.deb` packages and the apt repository.

**Milestone 2: configuration**

- Config engine, with schema forms for: `rpt.conf` node settings, `simpleusb`, `iax` general, `echolink`, registrations, `manager`.
- Raw editor for all files.
- Snapshots, history, rollback.
- First-run setup wizard.
- System page: Asterisk restart, log tail, disk.

### 9.2 Later

- USB radio audio tuning UI (RX/TX levels; live meters if feasible).
- DTMF functions, macros, telemetry, and scheduler editors.
- Allow/deny list manager (AstDB).
- Upstream `.dpkg-dist` merge view.
- Layout migration tool.
- Voter / RTCM.
- Plugins (e.g. M17 gateway management).
- **Want:** multi-node / multi-host status (one page showing nodes on several servers, as Allmon3 can) and fleet management.
- Backup/restore export.
- i18n.

## 10. Packaging and updates

- nfpm (pinned in the Makefile, `make deb` / `make debs`) builds `.deb` for arm64 and amd64 in GitHub Actions (`.github/workflows/`). Definitions live in `packaging/`.
- A signed apt repository hosted on GitHub Pages. A one-line install script adds the repo key and source, then runs `apt install`.
- Updates through normal `apt upgrade`; the in-app "update available" notice reads `latest.json` (see below).
- Release channels and process: versions are plain `X.Y.Z`. Every tag `vX.Y.Z` builds the debs and publishes them to the `beta` suite (and `latest-beta.json`), with a GitHub pre-release. A release is made by the `Promote` workflow (manual, input: version): the already built debs move from `pool/beta` to `pool/main`, `latest.json` is updated and the GitHub release becomes the full/latest release. No rebuild, so the tested package is the release; promotion refuses a version that is not in beta and is idempotent. The apt repo has two suites in one repository: `beta` indexes all of `pool/` (new builds in `pool/beta`, everything promoted in `pool/main`), `stable` indexes only `pool/main`, so beta nodes also get every release. Both suites are signed. Retention keeps the newest 5 per architecture in each pool directory separately (never the version just added or promoted). `latest.json` is the newest promoted version (format unchanged, existing installs read it), `latest-beta.json` the newest overall. Release notes come from the CHANGELOG section matching the version (`## [1.1.16]`). `install.sh` is refreshed only when the version being published is the newest in the pool, so a backport does not revert it. The 1.0.x line is maintained on the `release/1.0` branch (CI runs there; the release workflow runs on every `vX.Y.Z` tag and uses the tagged commit's script); a backport tag also goes to beta first and needs a promote. The `Remove from beta` workflow (dry run by default) deletes beta-pool versions by glob. Debian ordering still decides what apt offers; a node on a version higher than the suite offers is never downgraded automatically.
- The channel of a node is the suite in `/etc/apt/sources.list.d/nodespot.list`, so the web UI and `apt upgrade` agree. The helper has two fixed ops, `update-channel-stable` and `update-channel-beta`, that rewrite only the suite word of the `nodespot` line atomically (refused while an upgrade runs); its status reports the current `channel`. Updates tab has the switch (`PUT /api/updates/channel`); `install.sh` takes `NODESPOT_CHANNEL=beta` and otherwise keeps the suite of an existing source file. apt never downgrades, so switching beta to release waits for a newer release.
- Package contents: systemd units (server, helper socket and service), `/etc/nodespot/nodespot.conf`, `/var/lib/nodespot`, a firewalld service definition.
- Public home: one public repo, `w1jar/nodespot`. `main` is the source (README, `SECURITY.md`, `LICENSE`, issue forms in `.github/ISSUE_TEMPLATE`, screenshots in `docs/images`) and the releases are created there from the tags. The apt repository lives on the `gh-pages` branch of the same repo (GitHub Pages at `https://w1jar.github.io/nodespot`), written by the workflows with the built-in token via `packaging/apt-checkout.sh` (creates an orphan branch on the first release) and `packaging/publish-apt.sh`. The .deb's `/usr/share/doc/nodespot/copyright` (DEP-5) is the static `packaging/copyright`; the licenses of bundled third-party code are in `packaging/THIRD-PARTY-NOTICES`. Bug reports use the issue form; Settings > About links to it and `GET /api/diagnostics` provides a redacted support file (see API.md).
- Implemented: the public repo `w1jar/nodespot` (GitHub Pages) is produced by `packaging/publish-apt.sh` from the release workflow; the in-app update flow (Updates tab) calls `nodespot-helper` (`update-status|check|apply|channel-stable|channel-beta`), which runs the upgrade as a transient systemd unit. The helper also has fixed system-update ops (`os-check|os-status|os-apply|reboot|nodespot-restart`: only fixed apt-get/systemctl commands, never a package name or any argument from the web process; Updates tab). The System tab (1.3.4) adds fixed ops for hostname, time zone, NTP, power off, journal logs, service state and network status (`host-*`, `logs-read`, `services-status`, `net-status`, and the streaming `diag-collect` of the 1.3.4 diagnostics bundle, whose redaction rules are pure functions in `internal/diag/bundle_redact.go`; one Go file per feature under `internal/helper/sys_*.go`, API in `internal/api/handlers_system*.go` and `handlers_audit.go`, UI sections in `web/src/components/system/` listed in `sections.ts`). Otherwise it implements only update, config, backup and firewalld operations; the `/etc/asterisk` write and `asterisk -rx` operations of section 3.4 are still to come.
- `postinst` creates the `nodespot` user, the data directory, the config file (without overwriting an existing one), the self-signed certificate, and the helper's permissions.
- Config files are marked as conffiles so upgrades never clobber local edits.
- Package dependencies on `asterisk`/`asl3` packages: exact names are **To verify**.

## 11. Testing

| Level | Approach |
|---|---|
| Unit | Go tests for parser, resolver, schema, auth, store. |
| Golden | Round-trip byte-identical test over the anonymized config corpus in `testdata/configs/`. Corpus covers old flat layouts, templated layouts, radioless hubs, multi-node, and custom bridge nodes (e.g. USRP to an M17 gateway). Secrets are scrubbed before inclusion. |
| Integration | A fake AMI server replays recorded event and command-output transcripts. |
| Fuzz | Go fuzzing on the parser (never panic; round-trip invariant). |
| E2E | Playwright against a built binary with a fake AMI and a temp config tree. |
| CI | GitHub Actions: lint, test, build for both architectures (arm64, amd64), package smoke test. |
| Hardware | Manual checklist on a Pi (reload semantics, memory use, audio device handling, upgrade path). |

## 12. Repository layout

```
cmd/nodespot/          server
cmd/nodespot-helper/   privileged helper
internal/astconf/   parser, writer, resolver
internal/schema/    settings knowledge base
internal/ami/       AMI client
internal/api/       REST + SSE
internal/auth/      users, sessions, CSRF
internal/store/     SQLite
internal/schedules/ scheduled connects and disconnects (docs/SCHEDULES.md)
internal/system/    host metrics, logs, service control
web/                Svelte app
packaging/          nfpm, systemd units, postinst, apt repo scripts
testdata/configs/   anonymized real-world corpus
docs/
```

## 13. Open questions / to verify

| Topic | Question |
|---|---|
| Reload semantics | Which settings take effect with `module reload app_rpt.so`, `iax2 reload`, `dialplan reload`, usb channel driver reload, versus full restart? Verify on hardware; record per key in the schema. |
| `asl3-menu` and `custom/` | How does `asl3-menu` consume `custom/` and the `;MENU:` comments? Should nodespot coexist with it, and what happens if both edit the same file? |
| AMI clients | Exact config locations and formats of Allmon3 (believed `/etc/allmon3/`), AllScan and Supermon; whether `allmon.ini.php` is Allmon2/Supermon format as believed. Update their credentials automatically or only warn? |
| Node list source | Source, format, size, and update cadence of the ASL node list for lookup; local caching and offline behavior. |
| armhf support | Resolved: armhf was dropped while the stack needed ASL3 (arm64 and amd64 only). The native node has no such limit, so armhf is back (1.0.3) as a hotspot and network node for the Raspberry Pi Zero W: GOARM=6 build, published, promoted and pruned like the other architectures; no vocoder and no analog audio. |
| Minimum ASL3 version | Which ASL3 releases (and `app_rpt` output formats) are supported. |
| Helper transport | Socket activation versus polkit/sudoers: which is simpler and more robust on bookworm and trixie. |
| Multi-file atomicity | Is staged write plus snapshot rollback sufficient? |
| Memory budget | Does the binary plus SQLite stay within ~30 MB on a Zero 2 W under live SSE load? |
| Package dependencies | Exact ASL3 package names to depend on or recommend. |
