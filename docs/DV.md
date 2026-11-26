# Digital modes (DV)

> Removed: this describes a feature of the archived aslcp 2.x that needed Asterisk. nodespot runs its own node; see [NATIVE_NODE.md](NATIVE_NODE.md). Kept for reference.

Digital modes (DMR, YSF, P25, NXDN) run on the nodespot engine, `nodespot-dv`, a small service that ships in the nodespot package. aslcp 2.0 replaces the previous third-party digital voice stack with it: that stack is no longer installed, configured or restarted by nodespot.

> **You have to redo the digital-modes configuration in 2.0.** The BrandMeister, TGIF, DMR - Other, YSF, P25 and NXDN settings and your identity (callsign and DMR ID) are not carried over from the previous stack, and nothing is imported from its ini files. The previous stack is removed on upgrade, except md380-emu. See "Upgrading from 1.x" below.

Status in 2.0.0: the engine, the control socket, the vocoder link, the **Echo test** (`LOOP`, a local audio test), **BrandMeister** (`BM`, a native Rewind client), **TGIF** and **DMR - Other** (native Homebrew clients), **NXDN**, **YSF** and **P25** (native reflector clients) are all in. YSF carries voice in DN (V/D mode 2) and Voice FR (VW); P25 carries IMBE. All modes are available.

## Parts

```
browser  <-- REST + SSE -->  nodespot (user nodespot)
                               |-- ctl socket /run/nodespot-dv/ctl.sock --> nodespot-dv (user nodespot-dv)
                               |-- helper socket /run/nodespot/helper.sock --> nodespot-helper (root)
nodespot-dv <-- USRP UDP 34001/32001 --> Asterisk chan_usrp (the bridge node)
nodespot-dv <-- UDP 127.0.0.1:2470 --> md380-emu (the vocoder)
```

- `nodespot-dv` reads `/var/lib/nodespot-dv/config.json`. The root helper renders it (`dv-config`); nodespot never writes it itself.
- The control contract (newline-delimited JSON, ops `status`, `reload`, `mode`, `tune`, `unlink`, `subscribe`) is in `internal/dv/ctl/types.go`.
- The server keeps one subscription to the engine's event stream, only while digital modes are enabled, with a reconnect backoff of 1 s doubling to 15 s. It builds the snapshot (`GET /api/dv`), the `dv` SSE event and the Now Calling strip from it.

## Enable and disable

`nodespot dv enable` (as root) or the Enable button of the digital modes page runs one job, `nodespot-dv-enable`:

1. apt installs the md380-emu vocoder. Nothing else is installed on purpose; qemu-user-static comes along only because the md380-emu package depends on it (the vocoder is an ARM binary). On arm64 that package ships its own `/opt/md380-emu/qemu-arm-static` and its service runs that directly.
2. The previous stack that nodespot installed is removed (see "Upgrading from 1.x"); this step is also the retry if the removal after the upgrade failed.
3. The engine config is rendered and `nodespot-dv` is enabled and started.

If the md380-emu repository becomes unavailable, OS updates still work, with a warning, and digital modes keep running with the md380-emu already installed.

`nodespot dv status [--json]` shows the state; `nodespot dv disable` stops the engine and keeps its settings. The command and the installer variable of 1.x keep working as aliases. In the installer, `NODESPOT_DV=1` runs it.

Supported: Debian bookworm and trixie on amd64 and arm64. **32-bit (armhf) systems are not supported** (ASL3 has no armhf packages): the status reason is `armhf_unsupported`, the page says to reinstall with a 64-bit OS, and enable is refused with `409 not_supported`.

## Modes

| Mode | Id | In 2.0.0 | Tune value | Unlink |
|---|---|---|---|---|
| Echo test | `LOOP` | available, always on | none (started from setup, not tuned) | none |
| BrandMeister | `BM` | available | talkgroup (a trailing `#` makes a private call) | `4000` |
| TGIF, DMR - Other | `TGIF`, `DMR` | available | talkgroup | `4000` |
| YSF | `YSF` | available (DN, VW) | reflector id or `host:port` | `disconnect` |
| P25 | `P25` | available | talkgroup | `9999` |
| NXDN | `NXDN` | available | talkgroup | `9999` |

Availability comes from the engine (which modes the running build implements); while the engine is down the page falls back to a fixed list (Echo test, BrandMeister, TGIF, DMR - Other, YSF, P25 and NXDN). A mode that is not available answers `409 not_available` to mode and tune; an available one that is not set up answers `409 mode_disabled`. The Echo test is described below.

## The setup page

Digital Modes is one scrolling page in Settings (`#/settings/digital-modes`, no tabs) with these cards: *Engine* (status, services, Refresh, Disable, the *Resume after restart* switch, and the last enable job with a *Show log* toggle), *Identity*, *Bridge node* (a summary line with *Edit* once a node exists), *Audio* (the echo test and the AGC and gain settings), *User list (radioid.net)* (its switch, a summary and *Download now*) and *Callsign lookup (hamdb.org)*. The setup cards (Identity, Bridge node, Audio) appear once the engine is enabled; User list and Callsign lookup sit inside the Engine card then. The old `#/digital` link and the flat `#/settings/dv-engine`, `dv-identity`, `dv-bridge`, `dv-audio`, `dv-user-list` and `dv-callsigns` links redirect to this page. The networks are not there: each mode's Enable switch and settings are on the DV page (`#/dv`), where an admin clicks a mode button to open its panel. The switch saves the enabled state at once; if something is missing (the password, the NXDN ID, the identity) the panel opens, says what, and the switch stays off. Fields are saved with the panel's *Save*.

## Echo test

> The native node has its own `echotest` mode (see docs/NATIVE_NODE.md) and the engine's `LOOP` mode described here is gone from the node.

The Echo test is a local test with no network and no account: the engine records one transmission, waits about a second and plays it back through the bridge node as a received call from the pseudo callsign "ECHO TEST" (no talkgroup, no DMR ID). It proves the audio path of the node, USRP, the engine and the md380 vocoder in both directions. Its engine mode id is `LOOP`; it is always enabled while digital modes are enabled, so it has no Enable card, and a `LOOP` entry in an older `networks.json` is ignored. Not to be confused with the **BrandMeister Parrot** (TG 9990 on BrandMeister), a service of the real network that is called from the BrandMeister mode on the dashboard.

**Using it.** Settings, Digital Modes, *Audio*: press *Start echo test*, key up, talk, unkey, and you hear yourself back a second later. While it runs the dashboard card says "Echo test running" (it has no mode button there) and *Now calling* and *Last heard* show the playback as "Echo test". *Stop* switches back to the mode that was in effect before you started (the page remembers it; failing that, the last mode from `GET /api/dv/resume`, then the first enabled mode), and that mode's remembered talkgroup or reflector is tuned again, as when you switch modes on the dashboard. The Echo test is never remembered as the last mode, so a restart does not come back in it. Starting and stopping use `POST /api/dv/mode` (operator or admin).

## BrandMeister

BrandMeister is a native client of the Rewind protocol: the engine logs in to a BrandMeister master with your DMR ID and a hotspot password, and carries the audio between the bridge node and the network. There is no separate bridge program and no ini file to edit.

**Settings** (admin: Identity in Settings, Digital Modes, the network settings on the DV page; docs/API.md "Digital modes networks and identity"):

- *Identity*: callsign, DMR ID and an optional ESSID (2 digits, 01–99; the repeater ID is your DMR ID followed by it). The DMR networks log in with it. Clearing it disables them.
- *Master*: picked from a dropdown of the BrandMeister masters (entries named `BM_<id>_<Country>` in the DMR host list the node downloads daily, shown as "3104 – United States", sorted by id); pick the one closest to you. There is no port setting: the engine always uses 54006 (the port in the host list is the Homebrew port and is not used), and a port stored by an earlier version is ignored. If the list has not been downloaded yet, the card says so and shows an *Other address* field instead; *Other…* in the dropdown does the same for a master that is not in the list.
- *Password*: the password set in your BrandMeister SelfCare (hotspot security). It is write-only; the page shows only "Password set" and a Change button, and the password is never logged or returned.
- *Stay logged in when not in BrandMeister mode* (checkbox, off by default; the option `stay_logged_in` in the network's options): see "Login only while BrandMeister is the active mode" below.
- *Options*: the rest are under "advanced": the client version and the keep-alive, timeout and back-off timers. A blank option uses the engine default.
- *Enable* needs the address, the password and the identity; the page says which one is missing. Saving renders the engine config and reloads the engine, so a login attempt starts at once; the card shows the login state (pending, logged in, or refused with the master's reason).

**Login.** The engine logs in to BrandMeister as an Open DMR Terminal with your DMR ID and your BrandMeister password; no application ID is needed. A refusal (wrong password) shows on the card as "Login failed".

**Login only while BrandMeister is the active mode.** BrandMeister allows one Open DMR Terminal session per DMR ID, so a session that stays up locks your ID out of every other BrandMeister terminal app (a phone app, say). The engine therefore keeps the session only while BrandMeister is the active mode:

- Entering BrandMeister (a mode switch, a tune, a pin, or a resume after a restart that lands in it) starts the session and waits for the login, up to 12 seconds, before it tunes. If the master refuses the login or does not answer in time, the request fails with `502 login_failed` and the master's reason (engine error code `login_failed`), the session is stopped again and the engine is in no mode. The dashboard shows the message and the card shows the login state.
- Leaving it (switching to another mode, or tuning another mode) tunes TG 4000 (unlink), then ends the session with the Rewind close packet, so the master sees the terminal go away and the ID is free. While another mode is active BrandMeister never reconnects by itself.
- Until it is entered, the card and the status strip say "Not in use" (engine login state `idle`, gray). That is not a failure: the overview raises no alert for it.
- After a restart, "Resume after restart" follows the resumed mode: BrandMeister is logged in only if it is the mode that comes back (the tracker's mode switch or tune logs in like any other).
- The mode switch into BrandMeister takes as long as the login (normally a second or two) instead of being instant.

Turn on *Stay logged in when not in BrandMeister mode* to keep the old behavior (a session from the start of the engine, in every mode); it costs you the free ID. TGIF and DMR - Other always stay logged in. Changing the option re-renders the engine config and reloads the engine, like any other setting. In the engine config (`/var/lib/nodespot-dv/config.json`) this is the per-network flag `onDemand`; when it is absent it defaults to true for `BM` unless the option `stay_logged_in` is `"true"`, and to false for the other networks.

**Using it.** On the dashboard, choose BrandMeister, type a talkgroup and press Tune, or pick a favorite. The *Private call* switch (or a trailing `#`) calls the number as a private call. The dashboard's **Parrot** button (BrandMeister's Parrot, not the Echo test) calls 9990 as a private call: key up, release, and the master plays your transmission back as a private call from 9990 to you. That tests the whole path over the real network without disturbing a talkgroup. **Unlink** tunes TG 4000, BrandMeister's disconnect talkgroup.

**Upgrade from 1.x.** Nothing of the old bridge program is imported: pick the master and enter the password once on the BrandMeister card.

## Private bridge node and USRP

The engine talks to Asterisk through a **bridge node**: a private node with a USRP channel in `rpt.conf` (chan_usrp loaded in `modules.conf`). The page sets it up (`PUT /api/dv/private-node`, admin) as a plan that can be previewed with `dryRun`, then applied in this order:

1. `disconnect`: drop the node's links.
2. `asterisk_config`: write the node stanza, the `[nodes]` line and chan_usrp, with a history copy of each file.
3. `engine_config`: render the engine config with the node's USRP ports (defaults 34001 receive, 32001 transmit).
4. `asterisk_restart`: only when Asterisk must be restarted for chan_usrp.
5. `engine_reload`: `reload` on the control socket; if it does not answer, the helper restarts `nodespot-dv`.
6. `health`: the node is registered and the engine reports the USRP peer.

nodespot keeps a record of what it changed (`privatenode.json`, root only), so Remove and Rollback undo exactly that; a node that already existed can be adopted without being owned. The default display name of the bridge node is "Digital modes linked node". The link between a local node and the bridge node is on demand (connected when you tune) or permanent, with an optional "disconnect other links first". There is no setup write for identity, networks or credentials in 2.0.

`GET /api/dv/setup` is read-only: `bridgeNode` and the files `rpt_conf` and `modules_conf` with `present`, `writable`, `sha256` and `history` (the number of config history commits that touch the file).

## Host lists

nodespot downloads the DMR, YSF, P25, NXDN and M17 host lists itself (the lists the pickers show), once a day with a random jitter, only while digital modes are enabled, with conditional requests. They are kept in `<datadir>/hostlists`. `GET /api/dv/lists?mode=ysf` returns them with an `ETag`; `POST /api/dv/lists/refresh` forces a check (at most once a minute). A YSF, P25, NXDN or M17 tune is resolved against these lists on the server; a value that is not in the list is `422 unknown_target`. The M17 list (`M17_Hosts.txt`, tab separated `M17-XXX`, host, port) has no modules: the tune is the reflector and a module, `M17-XXX C`.

The YSF, P25, NXDN and M17 lists are reflector data provided by DVRef (https://dvref.com/) under CC BY 4.0. nodespot downloads and caches them as above, does not change them, and is not affiliated with or endorsed by DVRef. The DMR list is not DVRef data.

Callsign-only modes (YSF, P25, NXDN, M17): when the radioid.net list has no entry for a heard callsign, the *Look up callsigns on hamdb.org* switch in the Engine card (admin, off by default) asks hamdb.org for the name and place. It sends the callsigns heard on this node to hamdb.org, only real-looking amateur callsigns that were actually heard, one at a time, at least 10 s apart and at most 100 a day, and keeps the answers on the node (30 days, 7 days when not found). After 5 failures in a row or a 429/503 it backs off (at most 1 h). The name shows in Now Calling and under the callsign in Last heard; see docs/API.md "hamdb.org lookup".

The DMR user list (`DMRIds.dat`) is **not** downloaded: nodespot shows names and callsigns from the radioid.net list it already keeps for Now Calling.

## Resume after restart

With "Resume after restart" on, a tracker re-sends the last mode and tune after the engine restarts. It reads the engine over the control socket, not from a file. The "boot mode" hook of 1.x is gone: there is no file to write, the engine keeps its own state.

The tracker remembers the last tune of each mode and the last mode used. A disconnect (an unlink value, or the Unlink button) erases the tune of that mode but keeps the mode: after a restart the engine comes back in that mode with nothing linked (the log says "dv resume: YSF selected, nothing linked"). This is a plain mode switch, nothing is linked on the network (BrandMeister still logs in, since it is the active mode), and a disconnected target is never linked again. A mode that is switched off is skipped, and nothing is resumed more than a few minutes after the engine started.

A tune, a mode switch or an unlink made on the dashboard is remembered at once; changes made elsewhere are picked up by the tracker's next poll.

## When the bridge node is unlinked

The engine reaches the radio through the bridge node, so a target left tuned after the node was unlinked would only keep a network connection open. nodespot acts on its own link commands, at once and without a grace period. When nodespot drops the link of the bridge node (Unlink on a Dashboard tile or on the ASL page, the AllStar link panel of the DV card, a schedule, an exclusive connect that drops it, Disconnect all, or a change of the link settings that removes the permanent link) and no other local node is still linked to it, the DV target is dropped: the network is unlinked the way a mode switch leaves it, and BrandMeister logs out unless *Stay logged in* is on. The mode stays and the target is remembered. When nodespot links the bridge node to a local node again (the same panels, a schedule, a DV pin) and the engine has a mode but no target, the remembered tune is sent again, a second after the link command so that a tune sent by the same request (a DV pin) comes first. Pressing Unlink on the DV card is different: it forgets the target, so nothing comes back. Nothing happens when digital modes are off, when nothing is tuned (the echo test is no target), or when the link state is not known. Links made outside nodespot (DTMF from a radio, another node, Allmon) are not seen: the DV target is left as it is. A problem with the engine never fails the unlink or connect itself; it is logged. Each action is audited as `dv.bridge.released` and `dv.bridge.retuned` by the user who made the request (`schedule` for a schedule, `system` for the permanent link). The control request `unlink` takes `"release": true` for this; an engine older than the field refuses it, and the server then sends a plain unlink (the target is dropped, a BrandMeister session stays until the next mode switch).

## Upgrading from 1.x

**You have to redo the digital-modes configuration in 2.0.** Settings of the previous stack (BrandMeister, TGIF, DMR - Other, YSF, P25 and NXDN, and the identity) are not carried over and nothing is imported from its ini files, or from the ini files in a 1.x backup. Whatever the 1.4.0 panel itself stored in its own `networks.json` (enable switches, TGIF and DMR - Other address, port and password) is still read, so cards may show it filled in: check every card.

On upgrade from a version before 2.0.0 the package starts the transient unit `nodespot-dv-purge` once the upgrade has finished (`journalctl -u nodespot-dv-purge`; `nodespot dv enable` repeats it). It acts only on what nodespot installed, which the root-only install marker in `/var/lib/nodespot-dvstate` records, and does nothing on a node where nodespot did not install the old stack:

1. It deletes nodespot's own ini history copies of the old stack (`/var/lib/nodespot-dvstate-history` and the 1.x name of that directory).
2. It waits until dpkg is idle, then stops and disables the old stack's units (`analog_bridge`, `mmdvm_bridge`, `netcheck`, `ysfgateway`, `p25gateway`, `nxdngateway`, `stfu`) that exist.
3. It marks md380-emu and qemu-user-static as manually installed, simulates `apt-get purge` of the old stack's packages recorded in the marker, and refuses (removing nothing) if the simulation would remove anything else.
4. It purges those packages, checks that md380-emu is still installed, and drops the purged names from the marker.
5. The apt source and keyring stay, because md380-emu is installed from that repository and keeps getting its updates. md380-emu stays installed and working.

Nothing is removed that nodespot did not install, and md380-emu is never touched. A backup made on 1.x still restores (its ini files are ignored, see docs/BACKUP.md).

## Audit

Writes are audited as `dv.enable`, `dv.disable`, `dv.mode`, `dv.tune`, `dv.unlink`, `dv.favorite.*`, `dv.network`, `dv.identity`, `dv.private.*`, `dv.node_name`, `dv.link`, `dv.resume` and `dv.lists.refresh`; the two that follow a link command are `dv.bridge.released` and `dv.bridge.retuned` (the user who made the request, `schedule` or `system`). Audit lines never contain a password, key or token.

REST paths, the SSE event name and the settings keys keep their 1.x names for now; see docs/API.md.

## TGIF

TGIF is a native Homebrew (MMDVM) client: the engine logs in to a TGIF master with your DMR ID and the hotspot security password of your TGIF profile.

- *Master*: there is no address or port field. The master follows the DMR host list: the address and port of its first TGIF entry, else `tgif.network` port 62031 when the list is not downloaded or has no TGIF entry. The password is never taken from the list. A change of the list is picked up the next time the engine's config is written (a settings save, or the engine start); an address or port saved by an earlier version is dropped and has no effect.
- *Password*: write-only, like the BrandMeister password; the page shows only "Password set" and a Change button.
- *Options*: under "advanced" the repeater ID, color code, link by short key-up, unlink talkgroup, frequencies, location, description, URL and the keep-alive, timeout and back-off timers. A blank option uses the engine default. TGIF has no master options or timeslot field: it always uses timeslot 2.
- *Enable* needs the password and the identity (callsign and DMR ID). The card shows the login state.

On the dashboard, choose TGIF, type a talkgroup and press Tune, or pick a favorite. With *link by short key-up* on (the default), a short key-up on a talkgroup is enough for the master to link it.

## DMR - Other (Homebrew master)

DMR - Other connects to any Homebrew (MMDVM) master, for example a FreeDMR, DMR+ or hblink3 server. It has the same settings as TGIF plus the *Master options* string (sent to the master after login) and the *Timeslot* (default 2), but there is no default master. Pick one from the *Master* list (the DMR host list, without the BrandMeister and TGIF entries, which have their own cards); the address and port are filled in, and so is the list's password when the password field is empty and none is stored. Choose *Other...* to type the address and port (default 62031) yourself. The dashboard card names the master in its status line ("Logged in to <name>", or "Master <name>" before the login succeeds): the host list name when the saved address is on the list, else the address as typed; BrandMeister shows its master the same way, TGIF has a single master and shows none. On Homebrew masters the caller's DMR ID is not a callsign: *Now calling* and *Last heard* take the callsign from the radioid.net list and show the DMR ID when the list has no entry. A master address you saved earlier selects its list entry, or *Other...* when it is not in the list. When no password is set, `passw0rd` is used to log in (the field shows it as a hint); a password you type always wins. *Enable* needs the address and the identity. TGIF and DMR - Other can be enabled at the same time; each has its own login.

**DMR+ masters** (`DMR+_IPSC2-...` entries on port 55555 in the host list): one dynamic talkgroup is tuned at a time, by the short key-up that the tune sends; there are no static talkgroups. The master moves the talkgroup to its own timeslot (TG 759 sent on slot 2 can come back on slot 1), so DMR - Other receives calls on both slots (option `rx_any_slot`, default on for DMR - Other, off for TGIF); while a call is in progress, packets of the other slot are ignored. Your own transmissions stay on the card's slot. Now calling and Last heard show the slot (TS1, TS2). The master drops a key-up talkgroup after about 15 minutes without traffic (yours included); after 15 minutes without a call on it the dashboard card says so and offers a *Retune* button, which sends the same tune again. It never retunes by itself.

## NXDN

NXDN is a native reflector client. It has no password and no login: the engine registers with the reflector by sending polls, using the callsign of the identity. Set your *NXDN ID* (1-65535) in Options; it is required to enable NXDN, and erasing it turns NXDN off. Under "advanced" are the RAN, the local listen address and the poll and back-off timers. *Enable* needs the identity and the NXDN ID. Clearing a field and saving clears it (an empty option is dropped and its default applies). On the dashboard, tune a reflector talkgroup; unlink is `9999`.

## M17 (reflector client)

M17 is a native client for M17 reflectors (mrefd, urfd style) over UDP, default port 17000, carrying Codec2 3200 voice. The engine encodes and decodes Codec2 itself in pure Go (no extra service). Config (`NetConfig`): the node callsign comes from the identity; `address`/`port` (optional, a tune can give them), and the options `module` (A-Z), `reflector` (`M17-XXX`, used as the destination of your transmissions), `can` (0..15, default 0) and `suffix` (appended to the callsign, for example `N0CALL B`). A tune target is `M17-XXX C` (reflector, space, module).

- Link: CONN, wait for ACKN, resent with growing delays; a NACK (module missing or callsign refused) or a kick by the reflector (DISC) shows as a failed login and is not retried until the next tune. A new module or reflector sends DISC to the old link first. Every PING is answered with a PONG; 30 s without a PING or PONG relinks.
- RX: stream packets, two Codec2 frames each, grouped by stream id; the call ends at the last-frame flag or after 1.5 s of silence. Packets with a bad CRC are counted and still played. Encrypted streams are ignored.
- TX: a new random stream id per call, destination `M17-XXX C`, source your callsign, no META, no encryption or signature.

Tested against an in-process fake reflector and an echo reflector through the bridge; the protocol layer is checked against the mrefd sources. Not verified without a live reflector: acceptance of your callsign, suffix and destination form by real mrefd/urfd builds, the TYPE/CAN bits against live traffic, voice level and quality of the Go Codec2, behavior behind NAT and on busy modules. The dashboard panel, the host list and the settings card for M17 are not part of this stage.

## YSF (System Fusion)

YSF is a native reflector client for System Fusion reflectors (YSFReflector style). It has no password and no login: the engine polls the reflector to keep the link up and uses the callsign of the identity as the node callsign, so *Enable* needs the identity.

- The card is enable/disable only: there is no address or port. Enable it, then tune a reflector on the dashboard, picked from the YSF list. Nothing is linked when the engine starts. An address or port stored by an earlier 2.0 build is ignored and dropped.
- Under "advanced": the poll period, the link timeout and the call end timeout.
- Voice is received in **DN (V/D mode 2)** and in **Voice FR (VW)**; a call may use either. Your transmissions are always sent in DN. Wires-X is not supported.
- VW is IMBE-coded and decoded by the built-in pure-Go IMBE codec (see "Voice codecs"); DN uses the md380-emu vocoder.

On the dashboard, choose YSF, pick a reflector from the list (or type a 5-digit id or `host:port`) and press Tune; unlink is `disconnect`. The card shows "Reflector not answering" (a warning) when the reflector has sent nothing back, not even a poll reply, for the link timeout (15 s by default), and clears it as soon as anything arrives.

## P25

P25 is a native reflector client for P25Reflector style reflectors (UDP, the link P25Gateway and MMDVMHost speak). It has no password and no login: the engine polls the reflector to keep the link up, and the link is "ok" once the reflector answers. The callsign of the identity is the node callsign, so *Enable* needs the identity.

- The card is enable/disable only: there is no address or port. Enable it, then tune a talkgroup on the dashboard, picked from the P25 list, which names the reflector. Nothing is linked when the engine starts. An address, port or P25 unit ID stored by an earlier 2.0 build is ignored and dropped.
- The DMR ID of the identity is always the P25 unit ID sent with your transmissions.
- Under "advanced": the poll period, the link timeout and the call end timeout.
- Voice is IMBE (full-rate Phase 1), carried in both directions. The source callsign is not sent on P25, so a received call shows the unit ID.

On the dashboard, choose P25, pick a talkgroup from the list (or type up to five digits) and press Tune; unlink is `9999`. The card shows "Reflector not answering" in the same way.

## Audio level and AGC

The bridge sees the audio of both directions: from your node to the network ("Node audio") and from the network to your node ("Network audio"). In the Audio card of Digital modes setup, under the echo test:

- **Node audio AGC (to network)** and **Network audio AGC (to node)** are always shown: automatic gain control. The network audio AGC (to node) is on by default and the node audio AGC (to network) is off. It evens out the level of speech toward the target and never boosts silence or noise. It has no delay, and a limiter keeps loud audio from clipping.

Press Show advanced audio settings for the levels and fixed gains:

- **AGC target level (dBFS)**: the speech level the AGC aims for, -40 to -6, default -22.
- **AGC maximum gain (dB)**: the most it may raise a quiet voice, 0 to 30, default 18.
- **Node audio gain (dB)** and **Network audio gain (dB)**: a fixed gain of -20 to 20 dB, default 0. It is applied before the AGC.

With the defaults the audio from your node is not changed, and the audio from the network is levelled by the AGC; switch Network audio AGC (to node) off to pass it through unchanged. Changes apply at once, without a restart.

The Digital modes card shows two small level meters, "To network" and "From network", and a summary of the last transmission in each direction: the speech level, the peak, and a verdict. The verdict is Low when the speech is more than 10 dB under the target, High when it is more than 8 dB over it, and Clipping when any frame clipped; otherwise OK. The verdict uses the configured target even with the AGC off. With the AGC on, the summary also shows the level after it.

This is not the Audio levels meter: that one reads the radio receive level of the sound card on the node, while these meters read the audio inside the bridge, after the gain and the codec path of the digital modes.

To check your own level, tune the echo test (LOOP), transmit a few words, and read "To network" when it ends: a speech level near the target is right. The echo test plays your audio back, so you hear what others hear.

## Voice codecs

DMR, NXDN and YSF DN use AMBE+2 through the md380-emu vocoder; P25 and YSF Voice FR use IMBE, which the engine encodes and decodes itself in pure Go (no extra service). Every frame a network hands the bridge carries its codec, so one YSF call may be DN and the next VW. The bridge encodes your transmissions with the codec of the active network (P25: IMBE; everything else AMBE). A frame of the wrong length for its codec is dropped. IMBE is computed on the CPU of the node: a 20 ms frame (encode plus decode) takes well under a millisecond on a current x86 core. Measured on a Raspberry Pi 3B+ (arm64, Go build): encode plus decode about 2.4 ms per 20 ms frame (about 12% of one core), encode alone about 3.0 ms, decode alone about 0.85 ms, and barely changed (+3%) while also receiving DMR. The engine runs at most one IMBE encoder and one IMBE decoder at a time.

Codec2 3200 (used by M17) is also built in, in pure Go with no extra service. It works on the same 8 kHz, 20 ms frames as the USRP side (8 bytes per frame), so the bridge needs no resampling. A network that carries Codec2 reports `CodecC2`; the bridge encodes your transmissions to it and decodes what it receives to the node's audio, one encoder and one decoder at a time. Frames pass through PCM, so a Codec2 network works with the same node audio as any other. Measured on a Raspberry Pi 3B+ (arm64, Go build): encode about 314 microseconds and decode about 394 microseconds per 20 ms frame, that is about 1.6% plus 2.0% of one core per stream. `nodespot-dv bench -codec codec2` measures it on your machine (the default `-codec ambe` measures md380-emu).

Reference check: the Go Codec2 has a manual comparison against libcodec2 (c2enc/c2dec, speech samples from the Codec2 project). It is not part of normal CI. To run it, open the Actions tab, pick "Codec2 reference check" and use Run workflow. It reports bit-exact frames, differing parameter fields, decode SNR and a cross decode, to the job summary, and fails below 30 dB decode SNR or under 90% bit-exact frames (thresholds at the top of `cmd/c2check/main.go`).
