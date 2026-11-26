# nodespot changelog

Release notes are taken from the section of the tag being released
(`## [X.Y.Z]`) when the tag has no annotation.

## [0.0.1] - 2026-11-27

### Changed
- TGIF now requires the hotspot password, as BrandMeister does: without one the mode shows "Not set up" and cannot be made active. A stored password counts, so saving other fields never asks for it again.
- Modes > settings: a saved password has a Clear button (with a confirm) that deletes only that password. `PUT /api/modes/{mode}` accepts `"clear": ["password"]` for it; unknown or non-password keys are refused with `409 node_refused`.
- Modes > settings: "Clear settings" is always shown (disabled while nothing is saved) and asks for a confirm first. It works on the active mode too: the node drops the mode, then forgets its settings and remembered target.
- BrandMeister and DMR (other master): the "Master server" field is a dropdown of the masters in the downloaded DMR host list, plus "Other..." for a typed `host` or `host:port`. BrandMeister masters (`BM_<id>_<Country>`) read "3104 – United States" and sort by id. A saved address that is on the list selects its entry, any other value selects "Other...". A DMR pick fills in the address and port, and the list's password when the password field is empty and none is saved. Without a downloaded list the field stays plain text with a note. BrandMeister uses port 54006 unless you type `host:port`; the list's port is never used. TGIF still uses `tgif.network` port 62031 when no address is saved: the node cannot read the host list.
- `POST /api/hostlists/refresh` now also refreshes the AllStar node list and answers with the status of every list. A refusal (too soon, busy) is still `409`; a download that fails is `200` with the error in that list's status instead of `409`.
- Mode fields can carry `hostList` (`"bm"` or `"dmr-other"`), which makes the Modes page draw the list picker.
- Modes > Echo test: the Codec picker is now in the echo test card, with readable names (Plain, AMBE, IMBE, Codec 2, GSM, µ-law). It saves at once and the next recording uses it. With AMBE picked and no md380-emu vocoder, the card says so and links to the DMR settings, where the vocoder is installed. The echo test has no settings section any more; the playback delay stays at its saved value (default 1000 ms) and `playback_delay_ms` is still accepted by the API.
- Settings > Modes is gone. `/settings/modes` redirects to `/modes`, `/settings/modes/<mode>` to `/modes?mode=<mode>` and `/settings/modes/lookup` to Settings > Station > Caller lookup. Station, Radio and the other settings stay.
- The project is renamed from aslcp to nodespot (new repository https://github.com/w1jar/nodespot; the old one, https://github.com/w1jar/aslcp, is archived at 2.4). Packages, services, users, paths (`/etc/nodespot`, `/var/lib/nodespot`, `/run/nodespot-node`), environment variables (`NODESPOT_*`), the apt repository (https://w1jar.github.io/nodespot) and its signing key are all new. There is no upgrade path from aslcp: install nodespot fresh. This is the first nodespot release.
- The first-run setup asks only for the station, the radio interface and the optional DMR and NXDN IDs. Modes are set up later on the Modes page.
- nodespot is now a standalone AllStar node. It needs no Asterisk and no ASL3: flash a fresh OS, install nodespot, fill in one setup screen and you are on the air. This is a fresh-install release, not an upgrade over an ASL3 node.
- AllStar, EchoLink, BrandMeister, DMR (TGIF and the other masters), YSF, P25, NXDN and M17 are equal modes. One is active at a time, and its audio goes out to the radio.
- When the far AllStar node disconnects you or hangs up, the link stays down: the node does not redial, and the link is not restored at boot.
- Changing the radio wiring (PTT pin, PTT or COS polarity, SA818 port) restarts the node service so the change takes effect at once.
- Settings > Station > Radio is split into two tabs. **Radio** keeps the RF settings: the interface, the SA818 channel, the timers and delays, the station ID, and a collapsed Advanced section with the PL filter, the SA818 filters and the mic boost. The new **Audio** tab (`#/settings/station/audio`; the old `#/tune` link now opens it) has a tuning wizard, the AGC and a collapsed Advanced section with the static gain, the AGC's maximum gain and the sound chip's counters. Each tab saves only its own settings.
- Audio tuning is a two-step wizard. Step 1: key up a handheld and talk; a live receive meter with a green target band says Good, Too low or Too high, and the slider is the sound chip's capture level (or Receive gain when the chip has none). Step 2: send a test tone and set the playback level (or Transmit gain) so it is clear and as loud as other stations, by ear or with the echo test.
- AGC: both directions are now on by default ("Radio → network" was off), and each has its own target: `advanced.rxAgcTargetDb` and `advanced.txAgcTargetDb` (-40 to -6 dBFS, default -22). The old shared `advanced.agcTargetDb` is copied to both when the config is read.
- One switch, "Radio audio is flat (data port or discriminator)" (`advanced.flatAudio`), replaces the separate software pre-emphasis and de-emphasis switches. It turns both on. A config with `preEmphasis` or `deEmphasis` on is migrated to `flatAudio: true`.
- SA818 filters: with nothing stored, the defaults are now emphasis off, high-pass on and low-pass on (`AT+SETFILTER=1,0,0`). In Radio > Advanced they are the "SA818 filters" group: "Pre/de-emphasis", "High-pass (removes PL tone)" and "Low-pass".

- EchoLink: the station info is always sent, when calling a station and again when it answers. With the "Station info" field empty it is the callsign, name and location.
- The node now checks the station settings it ignored before: AGC targets must be -40 to -6 dBFS and maximum gain 0 to 30 dB (the AGC always clamped them, a save now says so), ID tail window and final-ID quiet time at most 600 s, CW pitch 200 to 2000 Hz and CW level -60 to 0 dBFS.

### Fixed
- SA818: the filter flags were sent inverted. The module's `AT+SETFILTER=<emphasis>,<highpass>,<lowpass>` takes 1 to bypass a filter and 0 to turn it on, and the node sent 1 for on. The flags still mean "on" in the settings and the API; the node now writes the inverse. A module programmed by an earlier build is programmed again at boot (the state file's version changed, so its old record reads as unknown).
- EchoLink: a playback that arrived faster than real time (the echo test parrot in bursts, or Wi-Fi bunching packets) came back on the radio with much of it missing, and no counter showed it. The mode's receive queue held one second and dropped its oldest frames when full without counting them. The queues now hold five seconds in the connection and thirty in the mode, and their drops count as `dropped` on the Modes card.
- EchoLink: the first word of a received transmission (the echo test's "Welcome") was cut. The node played network audio in the same 20 ms step it keyed PTT, before the transmitter was up. Network audio now waits for the key-up delay (`advanced.txOnDelayMs`, default 200 ms) after PTT; nothing is dropped.
- EchoLink: the echo test's greeting could lose its first word, or all of it, behind a home router. The echo test starts its greeting as soon as our connect packet reaches it, but the node sent nothing from its audio port until the station answered, and nothing at all when the station info was empty. A router lets a station's audio in only after we have sent from that port, so it dropped the greeting's first packets. The node now sends its station info from the audio port before the connect packet, again with each connect retry, and once more when the station answers.
- EchoLink: a station's audio right after our own transmission starts a new sequence stream, so a playback numbered just below what the station sent before is never dropped as late, even within the talk hang.
- Dashboard and Modes card: "Local station · sending to <mode>" rarely appeared. The web server updated `radio.rxActive` only on full status reads, which a carrier change did not trigger; the node now publishes an `rx` event when it changes.
- EchoLink: after the echo test's greeting, the playback of your own transmission never came back on the radio, and nothing was logged. The connection dropped any audio packet whose RTP sequence number was a repeat of, or up to 999 behind, the last one received, for the whole connection, and the playback is numbered below the greeting. Each received transmission now starts sequence tracking afresh, and within one only a duplicate or a packet up to 15 behind is dropped, counted as late.
- Settings > Station > Radio, Levels: the receive and transmit volume sliders showed 0% and could not be moved. The node reports and accepts levels as fractions (0 to 1) and the page treated them as percent. The sliders now show 0 to 100% and convert both ways; "n/a" still means the sound chip has no such control.
- Modes page: EchoLink favorites accept a callsign as well as a node number (such as `N1CALL-L`).
- The Modes page no longer says "not set up yet. Add its login in Settings" for every mode. A mode is ready when its own settings or the station fallbacks (callsign, DMR ID, NXDN ID) cover what it needs, and the echo test is always ready. When something is missing the message names it, such as "Set your DMR ID in Settings > Station or on this mode's page." or "EchoLink needs its password."
- YSF and M17 had no settings. Each now has a callsign field, and so do P25, NXDN and EchoLink (the station callsign is the default, and EchoLink adds -L). Settings that fall back to the station show its value as the placeholder with a hint such as "from station: N0CALL".
- AllStar: a link that connected but carried no audio. The node dropped any full frame whose subclass byte it could not decode (such as 0xff, which Asterisk accepts) before counting it in the IAX2 sequence. Its receive sequence number then never passed that frame, the far node retransmitted it and everything behind it (the link list, `!NEWKEY1!` and the voice) forever, and the node answered every copy with a VNAK. Every well-formed full frame for the call is now counted and acknowledged, even when its content is ignored.
- AllStar: gap handling is like Asterisk's. An old or duplicate frame is acknowledged again with its own timestamp and never VNAKed. A frame ahead of a gap earns one VNAK per gap (at most one per 100 ms while the gap stays open), and a peer that VNAKs repeatedly gets one retransmission per 100 ms.
- AllStar: "peer hung up" and "rejected" errors now carry the far node's cause text and code, such as "peer hung up: Normal clearing (cause code 16)".

### Added
- Test tone: Settings > Station > Audio, tuning step 2, "Send test tone" keys the transmitter with a 1 kHz tone for 5 seconds at the level network speech reaches the radio, with a Stop button. It waits for the key-up delay, is refused while the channel is busy, and gives way when anyone starts talking. New `POST /api/radio/tone` (`{seconds?}`, 1 to 10) and `DELETE /api/radio/tone` (admin); the node status has `radio.testTone` while it plays.
- EchoLink link counters on the Modes card, like AllStar's: packets received and sent, plus late, dropped, bad and send errors when any occur (status `link`, `proto: "echolink"`).
- EchoLink logging: `echolink: sending` and `echolink: sent N packets` for each transmission to the station, and with `nodespot-node -debug` a summary at the start and end of each talk spurt each way (sequence numbers and counts, never audio). A failed audio send is logged once per transmission.
- Dashboard and Modes card: "Local station · sending to <mode>" while the radio's carrier is going to the active mode, in every mode (node status `radio.rxActive`).
- Settings > Station > Host lists: one row per downloaded list (AllStar nodes, DMR, YSF, P25, NXDN, M17) with its entry count, last update and last error, and an admin Refresh button that downloads all of them (host lists at most once every 10 minutes; a refusal shows the server's message). New `GET /api/hostlists/status` (operator).
- Host lists: a failed download (no network or an unset clock at boot, the server down) is tried again after 10 minutes instead of a day later, and a manual refresh that downloaded nothing does not block the next one.
- Modes page: when the YSF, P25, NXDN or M17 list has not downloaded yet, the search says so and points to Settings > Station > Host lists instead of showing nothing.
- Audio: static gain each way (`advanced.rxGainDb` radio to network, `advanced.txGainDb` network to radio, -20 to +20 dB, default 0), applied before the AGC and soft-limited so peaks never wrap.
- Emphasis. SA818 radios: the module's filter flags are `radio.sa818Filter.{emphasis,highPass,lowPass}`, saved in the config and sent to the radio only when Program is pressed (or another explicit programming path runs), never automatically. Settings shows what the state file says was programmed, or "unknown until programmed". Radios with flat audio: software 75 microsecond pre-emphasis on transmit and de-emphasis on receive (`advanced.flatAudio`, off by default, 0 dB at 1 kHz).
- PL filter: `advanced.plFilter`, a 300 Hz high-pass (4th-order Butterworth) on received audio. Unset means automatic: on unless the radio is an SA818. Settings shows the effective value.
- Receive boost: `radio.rxBoost` sets the sound chip's microphone boost through the ALSA mixer. The switch appears next to the receive volume only when the chip has the control.
- Delays: `advanced.rxOnDelayMs` (0 to 1000, a carrier must last this long before it counts as a receive) and `advanced.txOffDelayMs` (0 to 2000, extra time the transmitter stays keyed after the hang time). Both default to 0.
- Key-up delay: `advanced.txOnDelayMs` (0 to 1000 ms, default 200, 0 turns it off), how long network audio waits after the node keys PTT. In Settings > Station > Radio, under Timers.
- EchoLink logs `echolink: received N packets (late L, dropped D)` at the end of each received transmission.
- Link counters: "not played N" on the Modes card (node status `radio.netDropped`) counts network frames thrown away because a local station had the receiver or the transmit timeout had locked the network out.
- Settings > Station > Radio has an "Audio and timers" card for settings that had no UI: the AGC in each direction (both on by default) with a target each (-40 to -6 dBFS, default -22) and maximum gain (1 to 30 dB, default 18), the transmitter hang time and transmit timeout (both ways), and the station ID's on/off, interval, tail window, final-ID quiet time, CW speed, pitch and level, and optional voice file. Defaults show as placeholders, empty uses the default, changes apply at once without a restart, and the card has an unsaved marker and a Revert button.
- The Modes page is rebuilt on the model of the old DV page. A button row covers every mode (AllStar, EchoLink, BrandMeister, TGIF, DMR, YSF, P25, NXDN, M17 and the echo test); the active one is highlighted and not-ready ones are marked. The selected mode shows its status (linked target, who is calling, the AllStar counters line), connect and disconnect with host-list or node search, the last tune, per-mode favorites (add, rename, remove, move earlier or later, connect, pin to the dashboard) and its settings in a collapsible section (station fallback placeholders, save, clear, the md380-emu Install button for AMBE modes, the not-ready message). Activating a mode is an explicit "Make active" action and shows a "Not confirmed" notice if the node has not confirmed the switch after 5 seconds. It works at phone width.
- The dashboard Pinned strip connects a pinned favorite with one tap and switches the mode if needed. The mode card has a Disconnect button.
- Each mode remembers its last tune (`tune.json` next to `node.json`) and reconnects to it when the mode becomes active, including at startup, until you disconnect on purpose. A drop or a failed connect keeps it. The "default reflector" and "default talkgroup" fields of P25, NXDN and M17 are gone; an old value seeds the remembered tune once, and old configs still load.
- Station DMR ID and NXDN ID (optional), on the setup screen and in Settings > Station. BrandMeister, TGIF, DMR and P25 use the station DMR ID, and NXDN the station NXDN ID, when the mode does not set its own.
- Install the AMBE vocoder (md380-emu) from the Modes page. When a mode that carries AMBE voice (BrandMeister, TGIF, DMR, YSF, NXDN or the echo test with the AMBE codec) is picked and the vocoder is missing or not running, an admin sees "Needs the AMBE vocoder (md380-emu)" with an Install button. The helper adds the md380-emu package repository with a pinned key, installs the package, keeps the service local-only and starts it. `install.sh` still does not install it. New admin API: `GET /api/vocoder` and `POST /api/vocoder/install`.
- The native node (`nodespot-node`, `nodespot-node.service`): one radio (the SHARI) and exactly one active mode, which is AllStar, EchoLink, BrandMeister, TGIF, another DMR Homebrew master, YSF, P25, NXDN, M17 or the echo test. The radio is separate from the networks. Whatever mode is active, its audio goes out to the radio and the radio's audio goes out to it. Selecting another mode drops the old one first, and networks are never bridged to each other. AllStar keeps its registration current in the background whenever it is configured. EchoLink logs in to the directory only while it is the active mode.
- Echo test mode (`echotest`) for setting levels: it records what the radio receives (up to 60 seconds, after the receive AGC), waits about a second and plays it back on the radio. A new transmission stops the playback and records again. The `codec` setting (plain, AMBE, IMBE, Codec2, GSM or ulaw) runs each frame through encode and decode, and only AMBE needs the vocoder. Each recording gets a level report (peak, voiced RMS, clipped samples, duration, and "clipping" or "too quiet" warnings) in the mode status, the node status and the app. The Radio tune page has a live input meter with a -20 to -6 dBFS target band (`GET /api/radio/input`), an Echo test card and a button to switch to the mode. It replaces the old DV loopback echo test.
- AllStar link diagnostics. With `nodespot-node -debug` every IAX2 full frame in and out is logged with its fields, and every dropped frame with the reason. The AllStar status carries link counters (voice frames received as full and mini frames, voice sent, VNAKs sent and received, retransmits, parse errors, dropped frames, gaps), shown in one compact line on the Modes page while linked.
- When an AllStar link attempt ends in a hangup or rejection within about 10 minutes of the node registering, the message says so ("registered 3 min ago; other nodes may refuse calls for up to ~10 min until their node lists update").
- A short playout buffer at the start of each received transmission, so network jitter does not cause gaps on the air.
- If unkeying the transmitter fails, the node keeps retrying and the status says so.
- The node handles the radio side itself:
  - the simplex logic: COS, PTT while the mode talks, a 500 ms hang, and a 3 minute transmit timeout both ways. The radio is not keyed after a local transmission ends;
  - AGC on the network's audio (on by default) and on the radio's audio (off by default);
  - the station ID. It goes out on the radio only, never to a network, and only after activity: within 10 minutes of the first transmission and once more when activity ends. Where it can, it rides the tail of a transmission instead of keying up again.
- The node config is `/etc/nodespot/node.json` (mode 0600). It holds the station (callsign, radio channel and levels, ID, the active mode and target restored at boot, advanced settings) and each mode's settings. Errors are reported in plain words, and the file reloads on SIGHUP or when it changes. Without the file the node runs idle.
- Control API on `/run/nodespot-node/control.sock` (JSON lines, no TCP listener). Ops: `status`, `modes` (never returns passwords), `save_station`, `save_mode`, `delete_mode`, `select`, `connect`, `disconnect`, `last_heard`, `now_calling`, `radio_levels`, `radio_tune`, `radio_program`, `reload` and `subscribe`.
- A udev rule (`60-nodespot-node.rules`) gives the node's user the CM108 HID interface and the CH340 serial port of the SA818.
- USB radio interface support with presets: the radio side is one driver for the CM108, CM108AH, CM108B, CM109, CM119 and CM119A sound chips (the ones chan_simpleusb supports). Pick a preset (SHARI, a generic USB interface on an external radio such as a URI-style board, or none for a radioless node) and override the PTT pin, COS and CTCSS input pins and their polarity if needed; the SA818 frequency and tone fields show only when the interface has an SA818. The detected chip and USB id show in the node status, the setup screen and the Radio tune page. A config with no preset keeps the SHARI behavior when a SHARI is detected.
- The web UI is rebuilt around modes. A first-run Node setup (shown when the node reports it needs one) asks for the station callsign, the radio frequency and CTCSS/DCS (hidden on a radioless node), the station ID on or off (the licensee is responsible for IDing), and collapsible sections for AllStar, EchoLink, BrandMeister and the other DMR masters, YSF, P25, NXDN and M17. Detected hardware is shown read-only, and saved passwords show "set" and stay empty unless changed.
- A Modes view with the modes as equal choices: pick a mode, pick a target (favorites, recent or typed), connect or disconnect. One mode is active at a time, and the audio goes to the radio whatever the mode.
- The Dashboard shows the station card, the active mode and target, who is talking, and one Last heard list across all modes.
- Settings pages per mode, a Station page, a Radio tune page for the native levels and the SA818 (with a Program button), a Caller lookup page, and a Node service page (status and restart).
- The demo and mock modes (`npm run dev:mock`, `nodespot -demo`) render every page, with `MOCK_STATION_SETUP=1`, `MOCK_NO_RADIO=1` and `MOCK_HOTSPOT=1` to try the first-run setup, a radioless node and the hotspot case.

### Changed
- `nodespot-node.service` is enabled and started on install and runs as its own user, `nodespot-node`.
- The web server runs on the native node only, over its control socket (`[node] socket` in `nodespot.conf`, default `/run/nodespot-node/control.sock`). It no longer talks to Asterisk. The API now covers the node status, the modes and their settings (passwords are write-only: reading only says whether one is set, and saving an empty password keeps the stored one), selecting a mode and target, disconnecting, favorites per mode, Last heard and Now calling across all modes, the radio tune page (levels and SA818 programming), and the status and restart of `nodespot-node.service`. Schedules pick a mode and target.
- Backups use format 2: the nodespot database, `nodespot.conf`, the node config and the SA818 settings. Mode passwords are only included when asked for. A restore fills a blank password from the node's current config and lists the modes that still need one (`missingSecrets`), which are left out. Backups made by aslcp 2.x (format 1, an Asterisk node) are refused with a message.
- Diagnostics collect the node config (passwords removed) and the `nodespot-node` journal instead of the Asterisk files.
- The installer no longer installs AllStarLink 3. `NODESPOT_DV` is still accepted and does nothing (digital modes are built in).
- Schedules, favorites, Now Calling, Last heard and the public page use modes and targets. A schedule's overlap check ignores the mode, because the node has one radio and one active mode.

### Removed (compared with aslcp 2.4)
- The digital voice engine `aslcp-dv`, its service and its Asterisk USRP bridge (`internal/dv/bridge`, `internal/dv/usrp`). The native node replaces them.
- Everything that needed Asterisk or ASL3: the AMI client, the Asterisk config editor and its history, the SimpleUSB tune page, the bridge and private node handling, the Asterisk service control and the ASL3 package checks, the `[ami]` section of `aslcp.conf` (an old file with it still loads), the `asl3` package recommendation, and the installer options `ASLCP_ASL3` and `ASLCP_ASL_TELEMETRY`.
- The aslcp user is no longer added to group `asterisk`.
- Not supported by the native node: incoming calls (it only calls out and listens on no public port), DTMF commands, hub features, autopatch, stats posting, and bridging networks to each other.
- The web UI pages for Asterisk, AllStarLink services, the node list, favorite statistics, the digital modes bridge and the private node.

## aslcp history (archived project)

Everything below belongs to the archived aslcp project (https://github.com/w1jar/aslcp), which nodespot continues. These version numbers are not nodespot releases.

## [2.4.2] - 2026-11-25

### Changed
- The top menu stands out more: its items are larger and bolder, and the current page is shown in the accent color with an accent bar on the header's bottom edge instead of a gray fill. The active Settings group button is now an accent outline instead of a solid button, so it no longer outweighs the top menu. The phone menu shows the current page in the accent color.

### Fixed
- Now Calling and Last heard now show a call that was already in progress when a target was tuned or the bridge node was linked, instead of waiting for the next caller. This covers DMR (BrandMeister, TGIF and the other Homebrew masters), YSF, P25, NXDN and M17: a stream joined mid-call starts a call from the first voice frames (the caller is filled in from what the frames carry, and BrandMeister shows the tuned talkgroup with the caller unknown), and the real header arriving later in the same stream does not start a second one. The call ends through the normal end or timeout.
- DMR rows in the DV Last heard table (BrandMeister, TGIF and the other Homebrew masters) now show the caller's name and place under the callsign, like YSF rows do. The name and place are looked up by DMR ID in the radioid.net user list; before, every caller that had a DMR ID skipped the lookup.

## [2.4.1] - 2026-11-25

### Fixed
- When ASLCP itself unlinks the digital modes bridge node (Unlink on a tile or the ASL page, the DV card link panel, a schedule, an exclusive connect that drops it, Disconnect all, or a link settings change) and no other local node is linked to it, the DV target no longer lingers: the server unlinks the talkgroup or reflector at once, and BrandMeister logs out unless "Stay logged in" is on. The mode and its remembered tune stay, and the tune is sent again when ASLCP links the bridge node again (a request that tunes its own target, like a DV pin, wins); Unlink on the DV card still forgets it. Links made outside ASLCP (DTMF, other nodes, Allmon) leave the DV target alone. Both actions are audited as `dv.bridge.released` and `dv.bridge.retuned`. The control request `unlink` gained an optional `release` field (an older engine refuses it and the server falls back to a plain unlink).

## [2.4.0] - 2026-11-25

### Added
- Dashboard node tiles have an Unlink button next to each linked node (operators and admins only). It disconnects that link (not permanently) without opening the tile, and has a 44 px tap target on touch.
- The Dashboard DV tile has an Unlink button on its tuned line, named after the target ("Unlink TG 91", "Unlink YSF-TEST", "Unlink M17-XXX C"), so it is not mistaken for a node Unlink (operators and admins only, shown only while a target is linked). It does the same as the Unlink on the DV card: it leaves the talkgroup or reflector, never the bridge node link, and does not open the tile. It has a 44 px tap target on touch.
- The public page and the chrome-less `/embed` view now look like the Dashboard (node tiles, the Now Calling strip and the Last heard table) but stay read-only: no pins, no Unlink or connect buttons, no system bar. New admin setting "Show digital modes activity" (`showDV`, default off) adds digital modes calls (mode, callsign, talkgroup or reflector, slot, time, and name and place only from the local caches) over the same public API and SSE (`dv` event, about 1/sec). It never sends the master or server host, login state, bridge node, DMR IDs, addresses or versions, and respects the callsigns setting. Public requests never trigger a HamDB lookup.
- The public Now Calling strip now matches the rest of the app: full width directly under the header, red while someone talks, and a muted "Last: ..." line from the most recent public activity otherwise. Clicking it opens the same details sheet with only public data (source, callsign, name, place, start, duration and a QRZ lookup button; never IDs). The app and the public page share one strip and one sheet component.
- The AllStarLink tab (`#/allstar`, menu label "ASL") holds what the dashboard had: schedules, the status table of each node, the connect bar, Favorites and the network list.
- The DV tab (`#/dv`, shown when the digital modes engine is enabled) holds the Digital modes card with the level meter, a card of mode buttons and the full Last heard list as a table in a card of its own. There is one button per network with its state and login status (a dot: green logged in, red login failed, amber logging in, gray disabled or not in use; BrandMeister outside its mode reads "Not in use"). An admin clicks a button to open that one mode's panel in place, with its Enable switch and settings, one panel at a time; clicking the button again closes it. For everyone else the buttons are plain status. Last heard is a table with the callsign, the mode, the target (a talkgroup, a YSF reflector name or an M17 module) and the time on the right.
- Pinned favorites: a pin button on each AllStar favorite and each DV favorite puts a one-tap button on the dashboard. An AllStar pin connects the selected local node to the favorite; a DV pin switches the mode, tunes the target and links the bridge node if it is not linked. Pins are saved on the server with the favorite, so they are shared across browsers and survive a reload; viewers see the pins but only operators and admins can pin or unpin. Deleting a favorite removes its pin. A pin on the dashboard shows a green dot while it is active: the AllStar favorite's node is linked to a local node, or the DV favorite's mode and target are what is tuned now.
- `pinned` field on `GET /api/favorites` and `GET /api/dv/favorites`, set with `PUT /api/favorites/{id}` and `PUT /api/dv/favorites/{id}` (`{"pinned": true}`). Database schema 7 adds the column; backups carry the pins with the favorites.
- The top menu is now Dashboard, ASL, DV (when the engine is enabled), Schedules and, for administrators, Settings, a text item like the others. Everything else moved into one Settings area, shown as a row of seven group buttons with a tinted strip of tabs under it for the tabbed groups, full width; on phones each row scrolls sideways.
- Settings has seven groups, `#/settings/<group>`: AllStarLink (tabs Asterisk files, Audio levels when the node has audio devices, Node list), Digital Modes (one scrolling page: Engine, Identity, Bridge node, Audio, User list, Callsign lookup, with the setup cards appearing once the engine is enabled, as before), Access (Users and Public page on one page), System (tabs Overview, Updates, Network, WiFi, Web port, ASL web services, Terminal), Maintenance (tabs Backup & restore, Logs & services, Diagnostics, Audit log), About and Account. A tab has its own link, `#/settings/<group>/<tab>`, so back, forward and bookmarks work; `#/settings` opens System, Overview.
- The user menu has Account (change password) and About for everyone, plus Public view and Sign out. Operators and viewers can open only those two settings pages. The phone menu lists the tabs (Settings for administrators among them), then Account, About and Public view.
- Renamed: Config is now Asterisk files, Audio Tuning is now Audio levels, Digital modes is now Digital Modes, ASL services is now ASL web services, and the DV card and page links that said Setup now say DV settings. "Setup" is kept for first-run setup.
- Old links keep working: `#/config`, `#/tune`, `#/digital`, `#/backup`, `#/system`, `#/system/<section>` and `#/updates` redirect to their group and tab under `#/settings`, and so do the flat `#/settings/<id>` links of the first Settings layout. The update badge now links to `#/settings/system/updates`.
- Download diagnostics left the About card; it is only in Settings, Maintenance, Diagnostics.

### Fixed
- The top navigation entry (desktop and mobile menu) reads "ASL" instead of "AllStarLink"; the route and page titles are unchanged.
- The Dashboard Last heard Mode column shows "ASL" instead of "AllStar" for AllStar rows.
- Connecting from a pinned favorite on the dashboard no longer reports "Node X did not appear in the links after 10s" when the link came up: the check now runs on every page, and a node that is already linked starts no timer.
- YSF, P25, NXDN and M17 now show Enabled or Linked instead of "Logging in". They have no login, but the engine marked every enabled one as logging in from its start and never cleared it until a reflector answered. It now reads `off` until a target is tuned, then `ok` while linked. The mode buttons, the Digital modes setup rows and the overview alerts treat these four as links: gray "Enabled", green "Linked" with the reflector or talkgroup, amber "Linking" while a link attempt runs. BrandMeister, TGIF and DMR - Other keep their login states.
- BrandMeister logs in only while it is the active mode again, which frees your DMR ID for other BrandMeister terminal apps: entering it logs in and waits for the master (a refused or unanswered login is reported, `502 login_failed`), leaving it unlinks and closes the session, and "Resume after restart" follows the resumed mode. A new "Stay logged in when not in BrandMeister mode" option keeps the session open. The card and the DV status strip show "Not in use" (login state `idle`) in between, which raises no alert.

### Changed
- The Lookup page and its menu entry are gone; a bookmarked `#/lookup` opens the Dashboard. The node search stays in the connect bar and the favorite dialog, and the `/api/lookup` endpoints are unchanged.
- The Schedules bar now also shows on the Dashboard and the DV page (same rules as on the ASL page).
- Digital modes settings: the Networks section is gone (networks are managed on the DV page, in the mode panels) and Identity now comes before Bridge node.
- Optional hamdb.org lookup for YSF, P25, NXDN and M17 callers the radioid.net list does not know: the setting "Look up callsigns on hamdb.org" (Digital modes, Engine card, admin, off by default) shows the caller's name and place in Now Calling (marked "via hamdb.org" in the details) and under the callsign in Last heard. It sends heard callsigns to hamdb.org, one request at a time, at least 10 s apart, at most 100 a day, with a 20 entry queue that drops extras, back-off on 429 and 503 (Retry-After, else exponential with jitter, at most 1 h), a 1 h pause after 5 failures in a row, and a disk cache (found 30 days, not found 7 days, 5000 entries). `GET` and `PUT /api/dv/hamdb`; `name` and `place` on the callers of `GET /api/dv/snapshot`; `source` on Now Calling calls.
- Last heard: a YSF target no longer repeats the Mode column ("YSF-TEST", not "YSF YSF-TEST"); the one-line Last heard on the Digital modes card keeps the "YSF" label.
- DV tab: the mode switch and the tune box are clearer and compact. The modes are 40 px buttons (the active one filled with a check, only enabled modes appear in the mode selector, with "No modes enabled." and a Setup link when there are none) and the talkgroup or reflector entry with its Tune button sits on the same row (on a phone it drops to its own line under the buttons); the favorite star sits right after Tune, so there is no second row. Small uppercase "Mode" and "Favorites" labels sit above the mode buttons and the favorites. There is no Private call toggle: end the ID with `#` for a private call (`9990#` reaches the BrandMeister Parrot). The favorites are now the same one-tap buttons as the dashboard's Pinned ones (name, target and a green dot while tuned), with the pin and the ⋯ menu attached. A YSF favorite shows only its name; the reflector address is in the button tooltip. On a phone the DMR tune box opens a keypad with `#` and `*` for private calls.
- The dashboard is now an overview: a tile for each local node (click one to open it on the AllStarLink tab), a DV tile, an alerts strip when something is wrong, the pinned favorites and one Last heard table for AllStar and DV together, newest first (the same table as the DV tab, with the time on the right; an AllStar entry shows mode AllStar and its node as the target). Click a callsign for a small menu with a QRZ lookup and, when the entry has a DMR ID, radioid.net; "Echo test", private calls and entries without a valid callsign are plain text. A node tile lists its first three links (node number and callsign or name, then "+N more") and, while the node is keyed, who is talking; the DV tile shows the last caller and how long ago. A transmitting node tile is red like the Now Calling bar (it was orange); receiving stays green.
- Last heard shows a YSF call as `YSF <reflector name>` (for example `YSF US-EXAMPLE`) instead of `TG <address>`; it falls back to `YSF <address>` when the reflector is not known. `GET /api/dv/snapshot` callers of a YSF call carry `mode` and `dstName`. DMR stays `TG 91 · TS2`.
- The digital modes bridge node gets no tile on the dashboard, since it only links DV to the node.
- The overview no longer warns "The bridge node is not linked to a local node." (the DV tab's AllStar Link panel still shows the bridge state). The "The bridge node link is down." alert stays for a permanent link that is down.

## [2.3.1] - 2026-11-24

### Changed
- The Digital modes card on the dashboard is redesigned: a status pill in the header, and two link boxes (the YSF, DMR, P25, NXDN or M17 target and the AllStar link) that each hold their own Unlink button, with the "Disconnect other links first" checkbox under the AllStar button.
- The mode buttons form one segmented control on the same row as the search and Go, the tuned favourite is highlighted, the level meter is compact (TX and RX side by side, the echo test hint only when the level is Low or High), and the Last heard line shows the newest entry.

## [2.3.0] - 2026-11-24

### Added
- Automatic gain control (AGC) on the digital modes bridge audio, in both directions, with a target level and a maximum gain. Network audio AGC (to node) is on by default and node audio AGC (to network) is off; an upgrade gets the network audio AGC on unless it is turned off in the Audio card.
- The node and network audio gains are now in the settings: the Audio card of Digital modes setup, with the echo test, under Show advanced audio settings.
- Level meters on the Digital modes card for both directions, with a summary of the last transmission (speech level, peak, clipping and a Low, OK or High verdict).
- `GET /api/dv/audio`, `PUT /api/dv/audio` and `GET /api/dv/meter`.

### Changed
- The Digital modes page is shorter and follows the setup flow, in one column: Engine (status, services, resume switch, the radioid.net user list and the enable log), Bridge node (a summary with Edit), Identity, Networks (one card per mode with its state, a summary, an Enable switch that saves at once, and settings that open on demand) and Audio.

## [2.2.1] - 2026-11-24

### Changed
- The M17 callsign suffix (for example `N0CALL H`) is now in the main M17 settings; the channel access number moved to Advanced.

## [2.2.0] - 2026-11-24

### Added
- M17 reflector support (test build). A pure-Go Codec2 3200 port (also in the bridge and the bench), M17 packets and callsign encoding, and an M17 reflector client in the digital modes engine.
- M17 in the panel: configuration, tuning to a reflector and module, the reflector host list and the API.
- M17 in the web UI, including M17 calls in the Now calling strip.
- The DVRef credit for the reflector lists is in docs/DV.md.
- A manual "Codec2 reference check" workflow (Actions tab, Run workflow) compares the Go Codec2 3200 encoder and decoder with libcodec2's `c2enc` and `c2dec` on standard speech samples. It is not part of normal CI.

### Changed
- The M17 Codec2 3200 decoder now follows libcodec2 1.2's arithmetic (postfilter, synthesis, LSP/Wo decoding) instead of the faster approximations of the fork it was ported from. Its output now tracks the reference decoder closely, 41-48 dB SNR on the standard test samples. The encoder is unchanged.
- The public install page and README say up front that the installer installs ASL3 (`asl3` and `asl3-menu`) first when it is missing, and that a freshly flashed ASL3 card must finish its first-boot setup and reboot before installing.

### Fixed
- P25 and NXDN calls in the Now calling strip were labelled "DMR · TG n"; they now show P25 or NXDN.
- Docs implied ASLCP installs qemu-user-static to run the md380 emulator. It is present only because the md380-emu package depends on it; on arm64 that package ships its own `/opt/md380-emu/qemu-arm-static` and its service runs that directly.

## [2.1.3] - 2026-11-24

### Fixed
- A fresh install with ASL3 now makes an already connected USB radio interface work without a reboot: the installer also applies ASL3's `usb` device rule (2.1.1 only covered `sound` and `hidraw`) and reloads the USB sound driver when ASL3's `lowlatency` setting differs from the running one. If either step fails, the summary still asks for a reboot.
- Fixed a data race between the digital-modes status stream and the dashboard snapshot (a login change could be read while it was being written).

## [2.1.2] - 2026-11-24

### Changed
- `ASLCP_ASL_TELEMETRY` is documented as `0`/`1` like the other install options (`0` turns ASL3 telemetry off, `1` back on). `off` and `on` still work. The install page now writes `ASLCP_ASL_TELEMETRY=0`.

## [2.1.1] - 2026-11-24

### Fixed
- After `install.sh` installed ASL3 on a running system, a USB radio interface that was already plugged in stayed unusable (no audio, Audio Tuning showed "USB device string … was not found") until the Pi was rebooted. The installer now applies ASL3's device rules right away.
- Audio Tuning explains a missing or not yet usable USB audio interface in plain words instead of "unexpected reply".

## [2.1.0] - 2026-11-24

### Added
- `install.sh` installs ASL3 and asl3-menu from the AllStarLink repository when they are missing. The repository key is checked against a pinned fingerprint first, and gpg is installed if it is missing. `ASLCP_ASL3=0` skips this, `ASLCP_ASL3=1` makes an unsupported release an error instead of a panel-only install.
- `ASLCP_ASL_TELEMETRY=off` (opt-in) turns ASL3 telemetry off at install, `ASLCP_ASL_TELEMETRY=on` turns it back on. Left unset, nothing changes. The install page has a checkbox for it.

### Removed
- 32-bit (armhf) builds: ASL3 only ships arm64 and amd64. Existing armhf installs keep working but get no further updates; reinstall with Raspberry Pi OS Lite (64-bit). `install.sh` refuses on a 32-bit system before changing anything, and the Updates and Digital modes pages tell armhf users to reinstall.

### Fixed
- DMR networks using the Homebrew protocol: retuning right after the network logged in could send two link bursts at once with mixed-up packet sequence numbers. They are now sent one at a time.
- The setup link printed by the installer could carry a stale code when the installer restarted ASLCP (port change, `ASLCP_TAKEOVER`, digital modes), so setup said "Wrong setup code". The setup code now stays the same across restarts until an administrator exists.
- `aslcp dv enable` (and the installer with `ASLCP_DV=1`) kept waiting for up to an hour after digital modes were enabled, then reported a failure, because it did not recognise the helper's "succeeded" job state.
- If the md380-emu package repository is unreachable or gone, checking for and installing OS updates (and re-running the installer) no longer fails; a warning says the digital modes repository could not be refreshed.
- After a restore, the digital modes engine kept its old settings: the Settings page showed the restored networks as enabled while the dashboard card did not offer them. A restore now applies the digital modes settings to the engine.

## [2.0.5] - 2026-11-23

### Added
- `install.sh`: `ASLCP_TAKEOVER=1` runs ASLCP on port 80 and turns off the other ASL web services in one step. It stops and disables Allmon3, the web server of the ASL web UIs (Apache, lighttpd or nginx), asl-web-ui and Supermon, only those that exist and are enabled or running, and prints each one. Asterisk and app_rpt keep running, and Cockpit is left alone. The units are recorded in `/var/lib/aslcp/takeover.list`.
- `ASLCP_TAKEOVER=undo` moves ASLCP back to its previous port and turns exactly those units back on.
- `ASLCP_PORT=80` is accepted (other ports below 1024 still are not). The installer writes the `port80.conf` systemd drop-in for it, opens the port in firewalld, and refuses to switch, naming the program, if something else still listens on port 80.
- The install page has the takeover option, its explanation and the undo command.

## [2.0.4] - 2026-11-23

### Changed
- The project moved to https://github.com/w1jar/aslcp and the apt repository, update check files and `install.sh` to https://w1jar.github.io/aslcp. The old `artw1.github.io` address no longer serves anything.
- Upgrading rewrites the apt source (`/etc/apt/sources.list.d/aslcp.list`) and an unmodified `[updates] url` in `/etc/aslcp/aslcp.conf` from the old addresses to the new one automatically and prints a note. The default update check URL is the new address.

### Fixed
- Installs still on the old address cannot see this release by themselves. Run once: `sudo sed -i 's#artw1\.github\.io/aslcp#w1jar.github.io/aslcp#g' /etc/apt/sources.list.d/aslcp.list`, then `sudo apt update && sudo apt upgrade`. If you set an explicit `[updates] url` in `/etc/aslcp/aslcp.conf`, change it to `https://w1jar.github.io/aslcp` too.

## [2.0.3] - 2026-11-23

### Changed
- TGIF: the master options, timeslot, address and port fields are gone; TGIF always uses timeslot 2, and its master follows the DMR host list (first TGIF entry, else tgif.network:62031). Values saved earlier are dropped and have no effect. DMR - Other keeps its fields.

## [2.0.2] - 2026-11-23

### Changed
- The radioid.net user list is now checked for updates daily instead of weekly.
- Digital modes: the remaining mentions of "NXDN unit ID" now say "NXDN ID".

### Fixed
- Digital modes: clearing the NXDN ID, or the Options of DMR - Other or TGIF, and saving now clears them. Before, the old value was kept.
- Digital modes: clearing the NXDN ID turns NXDN off; NXDN can't be turned on without an NXDN ID (the page says so and the API answers `422 incomplete`).

## [2.0.1] - 2026-11-23

### Fixed
- DMR - Other: after retuning, calls on the previous talkgroup are no longer played, shown or counted as activity. Retuning to another talkgroup reconnects to the master, so DMR+ drops the previous one straight away instead of keeping it for about 15 minutes.
- Digital modes: switching or tuning by hand is no longer refused while a call is in progress; it ends the call. Schedules and the resume after a restart still wait for the call to end.

## [2.0.0] - 2026-11-23

> **You have to redo the digital-modes configuration in 2.0.** The BrandMeister, TGIF, DMR - Other, YSF, P25 and NXDN settings and your identity (callsign and DMR ID) are not carried over from the previous stack, and nothing is imported from its configuration files or from the digital part of a 1.x backup. After the upgrade, open the Digital modes page and enter them again. The previous digital voice stack is removed on upgrade, except md380-emu, which stays installed and working.
>
> P25 and NXDN are implemented and tested against recorded traffic, but have not yet been tested on air.

### Changed
- Digital modes now run on the built-in ASLCP engine; the previous third-party digital voice stack is no longer used, and it is removed when you upgrade (see the note above), except md380-emu.
- "Enable digital modes" installs only the md380-emu vocoder (its package depends on qemu-user-static, which apt installs along with it). ARMv6 boards are not supported.
- `aslcp dv enable`, `disable` and `status` manage digital modes from the shell; the installer accepts `ASLCP_DV=1`. The old install command and installer variable are gone.
- The digital modes API moved to /api/dv.
- The setup page shows the bridge node and the state of `rpt.conf` and `modules.conf`, and holds the identity and the settings of the networks that are available.
- The YSF and P25 cards are enable/disable only: the reflector address and port fields are gone, and nothing is linked when the engine starts; tune a reflector or talkgroup on the dashboard. Addresses and ports stored earlier are ignored.
- P25 no longer has a "P25 unit ID" option: the DMR ID of the identity is always used. A stored value is ignored.
- The NXDN option "NXDN unit ID" is now called "NXDN ID".
- The local Parrot test mode is now the Echo test in Digital modes setup (Test audio), and no longer appears as a mode on the dashboard. It was easily mistaken for BrandMeister's Parrot, which is a service of the real network (TG 9990) and is unchanged. The echo test is always available while digital modes are enabled, so its Enable card is gone (stored settings for it are ignored), and its playback no longer pretends to be a call on TG 9990.

### Added
- **BrandMeister is available again, natively.** The built-in engine logs in to BrandMeister itself: master (picked from the BrandMeister master list), password, identity (callsign and DMR ID) and options are set on the Digital modes tab, with the login state on the card, a private call switch and a Parrot button (TG 9990) on the dashboard. BrandMeister logs in as an Open DMR Terminal with your DMR ID and BrandMeister password; no application ID is needed.
- **TGIF, DMR - Other and NXDN are available, natively.** TGIF and DMR - Other are Homebrew (MMDVM) clients with master address, port, password and options (TGIF uses `tgif.network:62031` when no address is set); NXDN is a reflector client that needs only the identity and, to transmit, your NXDN ID. Each has its own card, login state and dashboard button. Your 1.x enable state, address, port and password are carried over; a network that lacks something it needs stays off until you complete it.
- **DMR - Other has a master list.** The card now has a *Master* drop-down filled from the DMR host list (BrandMeister and TGIF are left out, they have their own cards); picking one fills the address and port, and the list's password when none is set. *Other...* keeps the manual address and port. An address you saved earlier selects its entry. If no password is set, `passw0rd` is used to log in, so a master that uses it can be enabled without one.
- The dashboard Digital modes card names the master that BrandMeister or DMR - Other is set to (its name in the host list, else the address you typed).
- **YSF (System Fusion) is available, natively, in DN (V/D mode 2) and Voice FR.** The built-in engine links to a YSF reflector picked from the YSF list on the dashboard (or typed as a 5-digit id or `host:port`); it needs only the identity. The card is enable/disable only (no address or port). Both DN (V/D mode 2) and Voice FR (VW) calls are heard; your transmissions are sent in DN.
- **P25 is available, natively.** The built-in engine links to a P25 reflector picked by talkgroup from the P25 list on the dashboard (unlink is `9999`); it needs only the identity. The card is enable/disable only (no address, port or unit ID); your DMR ID is the P25 unit ID. Voice is encoded and decoded on the node itself.
- `GET/PUT /api/dv/networks/{mode}`, `GET /api/dv/networks` and `GET/PUT /api/dv/identity`; the dv snapshot carries each network's login state.
- New Echo test (Digital modes setup, Test audio) echoes your transmission back through the bridge node a second later, so you can check the audio path of the node, the engine and the vocoder both ways without a network or an account. It plays back as a call from "Echo test".
- ASLCP downloads the YSF, P25, NXDN and DMR host lists itself, once a day, while digital modes are enabled.
- **DMR - Other works with DMR+ masters.** Calls of the tuned talkgroup are heard on either timeslot (masters may move a talkgroup to its own slot), Now calling and Last heard show the slot (TS1 or TS2), and after 15 minutes without a call on the tuned talkgroup the dashboard offers a Retune button, because the master drops an idle talkgroup.
- Now calling and Last heard show the radioid.net callsign of DMR - Other and TGIF callers (the DMR ID is shown when the list has no entry).
- Now calling shows BrandMeister calls, including after a retune, calls joined mid-transmission, and private calls such as Parrot replies.
- The dashboard shows "Reflector not answering" when a YSF, P25 or NXDN reflector does not reply.
- Digital modes resume after a restart: the dashboard's Digital Modes card comes back on its own, in the last used mode (nothing is linked if you had unlinked it), and a tune or unlink is remembered at once.

### Fixed
- Open dashboard tabs reload by themselves when the panel comes back on a new version, however it was installed.

### Removed
- The previous digital voice stack: on upgrade from 1.x the packages ASLCP installed for it are stopped and removed; nothing else is removed, and md380-emu stays (with its apt source, so it keeps getting updates).
- The uninstall of the previous stack, the BrandMeister, STFU and network credential pages, the identity page, and the service restart and rollback buttons of the previous stack.

## [1.4.0] - 2026-11-21

### Added
- **Enable/Disable on each DVSwitch mode card.** BrandMeister, TGIF, DMR - Other, YSF, P25 and NXDN each have their own Enable/Disable button, and the dashboard's mode buttons follow them: a disabled mode is greyed out there. After a fresh DVSwitch install every mode is off until you enable it.
- **BrandMeister, TGIF and DMR - Other are separate modes.** Each DMR network has its own card, its own button on the dashboard and its own remembered talkgroup and favourites, and any of them can be enabled at the same time. TGIF's card has its own password (set or cleared like the BrandMeister password); its master and port are taken from the DVSwitch host list (`DMR_Hosts.txt`), and if TGIF is not in the list, `tgif.network:62031` is used and the card says so. TGIF and DMR - Other share the DMR bridge: switching between them on the dashboard restarts only the DMR bridge, and only when it is logged in to the other network; the dashboard shows the progress and the login result. Disabling a network keeps its settings.
- A notice card under Identity explains that DVSwitch starts in the last mode after a restart, and holds the "Resume after restart" switch with the last used mode and talkgroup or reflector.

### Changed
- **No more startup modes.** DVSwitch now always starts in the mode you used last, whether "Resume after restart" is on or off; with it on, the last talkgroup or reflector is tuned again too. The startup mode choice and the startup talkgroup and reflector fields are gone, and the gateways no longer link a startup reflector by themselves. ASLCP only updates the start mode in Analog_Bridge.ini when the mode changes, and does not restart anything for it.
- The Enabled Modes card was removed; its switches are now the Enable/Disable buttons on the mode cards. The DVSwitch page shows the BrandMeister, TGIF and DMR - Other cards side by side, then YSF, P25 and NXDN.
- BrandMeister's "Clear BrandMeister connection" and DMR - Other's "Clear DMR connection" are replaced by Disable. Disabling BrandMeister stops STFU and keeps its settings, so Enable brings it back without typing the password again.
- The update carries over each mode's Enable state and the DMR settings from 1.3, so nothing needs to be enabled again.
- The DMR - Other host picker no longer lists TGIF masters (TGIF has its own card).
- The settings of every DMR network (TGIF and DMR - Other with their passwords, and the Enable state of BrandMeister, TGIF and DMR - Other) are part of a DVSwitch backup, so a restore brings them all back. Older ASLCP versions cannot restore a backup that holds them.

## [1.3.9] - 2026-11-21

### Fixed
- Now Calling no longer shows DVSwitch traffic while the DVSwitch node is not linked to a local node.

## [1.3.8] - 2026-11-21

### Fixed
- The page no longer jumps when the Now Calling strip changes from a call to Last heard.

## [1.3.7] - 2026-11-21

### Fixed
- Now Calling shows an AllStar node's callsign, description and location instead of looking the callsign up as a person. The radioid.net user list now applies to DMR and YSF talkers only.

### Changed
- The Now Calling strip is shown on every page, not only the dashboard.

## [1.3.6] - 2026-11-21

### Fixed
- The public status page no longer gets stuck on "Loading..." when a node, link or transmission is listed twice, for example several unidentified links. The linked-network list shows such entries once.
- The DVSwitch bridge node is labelled "DVSwitch linked node" (or the name set under DVSwitch) in the public status page's links, network list and recent activity, like on the dashboard.

## [1.3.5] - 2026-11-21

### Added
- **Now Calling** strip on the dashboard, directly under the header (logged-in users only). While someone talks it is a red strip with a pulsing dot (no animation if your device asks for reduced motion), "Now calling" with the source (AllStar, DMR with the talkgroup, or the YSF room), the callsign, first name and place, and a running timer. Only one talker is shown, the one that keyed last. When it is quiet, a grey line shows who was heard last and how long ago. Tap either one for the details: source and link, callsign, name, place, DMR ID, start and duration, a QRZ lookup link, a radioid.net link for digital calls, and the last 5 talkers. It is a bottom sheet on phones and a dialog on larger screens. Nothing is shown until something has been heard. The last talkers are kept by ASLCP, so a page reload does not lose them.
- Talkers on the digital side of **DVSwitch** are read from the BrandMeister (STFU) and Analog_Bridge logs, including YSF. AllStar callers come from the keyed link (the DVSwitch bridge link is left out).
- **radioid.net user list**: the strip looks up name and place of DMR talkers by ID and YSF talkers by callsign. The list (`user.csv`) is downloaded to the data directory, on by default, refreshed weekly, and kept if a download fails. An administrator can turn it off, see when it was updated, how many entries it has and the last error, and press Download now, under DVSwitch.

### Changed
- The demo mode (`--demo`) shows sample talkers in the Now Calling strip.
- The demo mode keys only one link at a time across its nodes, like a real system, so the Now Calling strip shows one talker that matches the keyed row.
- On phones and narrow windows (below 1280 pixels) the menu is behind a menu button at the top right; the page no longer scrolls sideways.

## [1.3.4] - 2026-11-20

Versions 1.3.0 to 1.3.4 are released together as 1.3.4.

### Added
- A **System** tab for administrators, with a list of sections on the left (a menu on phones): Overview, Updates, Network, WiFi, ASL services, Web port, Terminal, Logs and services, Diagnostics, and Audit log. Sections are grouped and in a fixed order.
- **System, Overview** shows and changes the hostname (asks for confirmation), the time zone and network time (NTP), and has Reboot and Shut down buttons with confirmation. A tool that is missing on the node shows "not available".
- **System, Network** shows interfaces, addresses, the default gateway and DNS servers (read only).
- **System, Logs and services** shows the state of the Asterisk, ASLCP, network and DVSwitch services and the recent log lines of each, 50 to 1000 lines, optionally limited to the last hour, day or boot.
- **System, Audit log** lists who did what, newest first, with filters by user and action. Hostname, time zone, NTP, shut down, ASL services, web port, WiFi and terminal changes are recorded in it, with the terminal's start, detach, reattach and end (who, how long, which Linux account, why it ended). Passwords and what is typed or shown in the terminal are never recorded.
- **System, ASL services**: turn Cockpit, Apache and Allmon3 off or back on with one switch each, with a confirmation that says what you lose. Services are only stopped and disabled, never uninstalled. Turning Apache off offers to turn Allmon3 off too, because Allmon3 answers on ports 16080 and 16700+ on every address even without Apache. You can also close or reopen the matching firewalld services. The section shows which of ports 80, 443, 9090, 16080 and 16700+ are really listening.
- **System, Web port**: move ASLCP to port 80 (and back) from the web UI. It is offered only when port 80 is free, so Apache has to be off first. The page shows the new address; if the new address is not opened within 60 seconds, ASLCP goes back to its old port by itself. The old port is not kept open.
- Turning Apache back on is refused while ASLCP uses port 80, with a button to move ASLCP back.
- **System, WiFi** (administrators): scan for networks, add one (with or without a password), remove one, change the order in which saved networks are tried, and connect now. A node without a WiFi device or without NetworkManager shows "not available". The section warns before you change the network you are using to reach ASLCP.
- A **setup hotspot**: when the node has had no network for 3 minutes and no Ethernet cable, it opens a WiFi network named `ASLCP-<hostname>` so you can reach the web page and fix the settings. It stops by itself when a saved network connects. Administrators can change the delay or turn it off in System, WiFi; the choice is kept.
- A warning in the web page while you are connected through the hotspot: the network is open, so others nearby can see your login. Use it only to set up WiFi.
- **System, Terminal** for administrators: a Linux login prompt in the browser (xterm.js, loaded only when you open that section). It is off by default; an administrator turns it on in the same section. You log in with a Linux account name and password, and the shell runs as that account; no root shell is handed out.
- A terminal session closes after 15 minutes without typing, at 8 hours at the latest, when you log out, when you turn the terminal off, or when you press Disconnect. At most two sessions at a time (a detached session counts).
- Closing or reloading the page does not end the terminal session: it is detached, and the same tab reattaches on its own with the recent output. A detached session keeps running for 2 minutes, and for as long as `apt`, `apt-get`, `aptitude`, `dpkg` or `unattended-upgrade` runs in it (at most 2 hours), so `apt upgrade` typed in the terminal finishes safely even though it restarts ASLCP. A restart of the web server or the helper also only detaches it.
- Your own detached sessions are listed in the Terminal section with Reattach (the recent output is replayed) and End; an administrator can end any session. The list updates by itself every few seconds and right after a disconnect.
- The terminal runs in its own systemd unit (`aslcp-term-<id>`), so a terminal user sees the real `/home` and `/tmp`.
- **System, Diagnostics**: one button downloads a `.tar.gz` for asking for help. It holds the ASLCP, Asterisk and ASL3 package versions, the recent log lines of each service, the network summary, disk and memory, service states and your configuration files, plus a `README.txt` that lists every file and how many values were hidden in it.
- Passwords and other secrets are replaced by `<redacted>` before the file is built: Asterisk, AMI, IAX and registration passwords, EchoLink and DVSwitch passwords, ASLCP's own secrets, and WiFi keys. The password file, private keys, the ASLCP database and login sessions are never included.
- A checkbox, on by default, also hides your callsign and your public IP address (`<callsign>`, `<own-ip>`). The page lists what is included and what is left out before you download. Read the files before you post a bundle in a public place.
- Each download is written to the Audit log. Only one bundle is built at a time, the file is limited to 12 MB before compression, and building stops after 90 seconds.

### Changed
- Updates moved into the System tab. Old `#/updates` links still work.
- Reboot moved from the footer to System, Overview.
- `apt purge aslcp` turns back on the services ASLCP turned off (and only those) and removes the port 80 drop-in. `apt remove` leaves everything as it is.

## [1.2.7] - 2026-11-20

### Changed
- The one-line installer prints the setup link once, at the end. Before, the package printed it right after installing ASLCP as well, ahead of the port change and the DVSwitch install.

## [1.2.6] - 2026-11-20

### Changed
- The install page uses the control panel's logo.

## [1.2.5] - 2026-11-20

### Changed
- The install page (artw1.github.io/aslcp) has a new look matching the control panel, an install options builder (DVSwitch, port, channel) and Copy buttons.
- Installer: `ASLCP_PORT` takes 1024-65535; a lower port is refused (ASLCP cannot listen on it).

## [1.2.4] - 2026-11-20

### Fixed
- Restoring a backup onto a new node no longer leaves the DVSwitch bridge node as "not added by ASLCP". New backups carry ASLCP's record of the bridge node, so after a restore you can still change its number, remove it, and have the uninstall take it out. For a backup made before this version, or a bridge node made by hand, the DVSwitch page offers "Manage this bridge node with ASLCP". Older ASLCP versions cannot restore a backup made with this version.
- Restoring a backup no longer changes the web port: the node keeps the port it was installed with (`listen` in `/etc/aslcp/aslcp.conf`). Before, a backup from a node on another port moved ASLCP to that port and the page stayed on "ASLCP is restarting…".
- Restoring a backup onto a node installed with DVSwitch (`ASLCP_DVSWITCH=1`) now starts the DVSwitch services with the restored settings. Before, they stayed stopped until you opened the setup page or pressed Start.
- `apt purge aslcp` also deletes the config history (`/var/lib/aslcp-config-history`) and ASLCP's DVSwitch state (`/var/lib/aslcp-dvswitch`, `/var/lib/aslcp-dvswitch-history`, which hold copies of the DVSwitch passwords). `apt remove` keeps them as before.

### Added
- `POST /api/dvswitch/private-node/adopt` (admin): ASLCP takes over a bridge node that looks exactly like the one it adds but has no record. It changes no file and restarts nothing (docs/DVSWITCH.md).

## [1.2.3] - 2026-11-20

### Changed
- Installer: it refuses to run (and changes nothing) while ASL3 is still finishing its first-boot setup or while another package manager is running, instead of failing on the dpkg lock or being cut off by the first-boot reboot. It tells you to wait and run it again; a pending reboot only produces a warning.
- Package operations (install and update of ASLCP, DVSwitch and STFU, OS upgrade) wait up to 10 minutes for the apt lock instead of failing.
- The firewalld rule and the setup link in the install messages follow the configured port: the `aslcp` firewalld service for 8080, a plain TCP port for any other port. The firewall status in the diagnostics report shows whether the configured port is open (runtime or permanent configuration).
- Installer: it no longer asks for the administrator account in the terminal. It prints a one-time setup link instead, which opens the first-run page where you can create the administrator or restore a backup from an existing node.

### Added
- `aslcp admin setup-code` shows the one-time setup code again (until an account exists).
- Installer: `ASLCP_PORT=<1-65535>` picks the web port at install (for example `curl -fsSL https://artw1.github.io/aslcp/install.sh | sudo ASLCP_PORT=8181 sh`). It sets `listen` in `/etc/aslcp/aslcp.conf` (an address part is kept), restarts ASLCP and opens the port in firewalld. A bad value is refused before anything is installed.
- Installer: it warns, naming the program, when the web port is already in use, and says so when ASLCP did not start, instead of only showing the setup-code hint.
- Installer: `ASLCP_DVSWITCH=1` (or `yes`, `true`) also installs DVSwitch once ASLCP is running, and can be combined with `ASLCP_PORT`. If the DVSwitch install fails the installer warns and still finishes.
- `sudo aslcp dvswitch install` installs DVSwitch from the terminal (the same install as the DVSwitch page) and follows the install log until it ends.
- Restoring a backup onto a node without DVSwitch keeps the backup's DVSwitch settings and applies them automatically when you install DVSwitch, then starts the DVSwitch services, so no second restore is needed. The DVSwitch page shows the pending settings with a Discard button (`DELETE /api/dvswitch/restore-pending`; the status carries `restorePending`).

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- DVSwitch: an uninstall (or an install) that finished successfully could show as failed on the DVSwitch page.
- Restore preview: DVSwitch packages are no longer offered under "Packages to install" with a plain `apt install` command (DVSwitch is installed from the DVSwitch page) and no longer listed under "Other software".

## [1.2.2] - 2026-11-18

### Added
- DVSwitch: BrandMeister and DMR - Other side by side. The panel has a "BrandMeister" mode (through STFU, BrandMeister's Open DMR Terminal protocol) next to "DMR" (DMR - Other: any other DMR network through MMDVM_Bridge). STFU logs in to BrandMeister only while BrandMeister is the active mode (unless "Stay logged in" is set), so it does not take the connection from a phone app that uses the same DMR ID; switching takes a moment and needs no restart. Each has its own remembered talkgroup, favourites and labels (for example "BrandMeister TG 91" and "DMR TG 1"), and Parrot and private calls are offered on both. The panel offers BrandMeister once it is set up.
- DVSwitch setup: a BrandMeister section with a master picker (the BrandMeister masters of the host list, or type a host name; port 54006), the hotspot security password (write-only), talker alias, start talkgroup (default 4000), the login status and the last transmission heard. Saving checks the login; STFU then runs only while BrandMeister is the active mode, unless you tick "Stay logged in when not in BM mode" (default off). "Clear BrandMeister connection" stops it. While it is stopped on purpose the status reads "Stopped (not in use)". Switching to BM waits up to 10 seconds for the login and warns if it does not appear. BrandMeister SelfCare lists the connection under DMR Terminals as "Open DMR Terminal". STFU logs in with the 7-digit DMR ID (no ESSID).
- DVSwitch setup: the DMR master section is now "DMR - Other". Its picker leaves out BrandMeister masters, a BrandMeister master is refused there (with a warning when one is already saved), and "Clear DMR connection" turns it off. "Clear password" stays.
- DVSwitch: STFU is installed with DVSwitch (left stopped until BrandMeister is set up) and removed by the uninstall. Older installs keep the "Install STFU" button.
- DVSwitch: Switching modes now disconnects the mode you leave; it reconnects when you switch back.
- DVSwitch: BM Parrot (talkgroup 9990) is called as a private call, which is the only way it answers. The DVSwitch panel has a Parrot button and a "Private call" checkbox for DMR (a trailing `#` on the number does the same). A private call is never remembered or resumed after a restart. Verified through STFU; the MMDVM_Bridge path is not yet verified.
- DVSwitch: the bridge node's "RX keyed" line shows who is talking (for example "RX keyed · DMR TG 91 · N0CALL"), and the DVSwitch panel has a Last heard list of the last five callers. The callsign is looked up by DMR ID in the MMDVM_Bridge list when the log has none (as with STFU); without a callsign the DMR ID is shown. The Parrot's reply shows as "Parrot (private call)". The caller is shown while the bridge node is keyed.
- DVSwitch setup: Hotspot info fields (Location, Description, URL, Frequency, optional Latitude and Longitude), written to the `[Info]` section of `MMDVM_Bridge.ini` with the identity save (one frequency for receive and transmit, typed in MHz or Hz). The page warns while the stock sample values are still in place. MMDVM_Bridge is restarted after a change, like other identity edits.
- API: `PUT /api/dvswitch/brandmeister` takes `stayLoggedIn` and returns `bmLogin`; the setup carries `brandmeister.stayLoggedIn`; mode and tune replies carry `bmLoginPending` and `stfuStopFailed`; `dmr-status` `bm.state` can be `idle`.
- API: `PUT` and `DELETE /api/dvswitch/brandmeister`, `DELETE /api/dvswitch/dmr-connection` and `POST /api/dvswitch/stfu/install` (admin). The DVSwitch status carries `stfu`, the setup carries `brandmeister` and `dmr.enabled`/`dmr.pointsAtBm`, `GET /api/dvswitch/dmr-status` carries `via`, `rx` and `bm` (BrandMeister's login), the snapshot carries the `BM` mode, `caller`, `heard` and `bridge.private`, and a mode switch reply carries `left`. `PUT /api/dvswitch/credentials` refuses a BrandMeister master (`400 bm_master`). `PUT /api/dvswitch/identity` takes `info`; a DMR or BM tune accepts a trailing `#`.

### Security
- STFU runs as root and writes the BrandMeister password to its log when it starts. ASLCP drops every log line that mentions a password before it reads the log, and keeps `/var/log/dvswitch/STFU.log` and its rotated copies readable by root only: the permissions now persist across log rotation (a systemd drop-in gives STFU `UMask=0077`; it is removed when you clear the BrandMeister connection or uninstall DVSwitch).

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- DVSwitch: switching to a mode with no remembered target could show the previous mode's value as a talkgroup (a YSF reflector showed as "DMR TG 76"). The new mode is now tuned to its own "disconnected" value, and the panel never shows another mode's value as a talkgroup.

## [1.2.1] - 2026-11-18

### Added
- DVSwitch bridge node name: give the bridge node a display name (default "DVSwitch linked node") on the DVSwitch tab. It saves at once with no Asterisk restart and shows in the header of the bridge node's section in the connection status table (and on the DVSwitch panel).
- Connection status table: the bridge node's "RX keyed" line shows, live, the mode and the talkgroup or reflector the bridge is tuned to (for example "RX keyed · DMR TG 3100"). It is no longer a badge on the DVSwitch panel.
- API: `PUT /api/dvswitch/private-node/name` (admin); the DVSwitch snapshot and setup carry `bridgeNode.name`.

### Changed
- Dashboard: the Schedules card is now the first card, above the connection status tables.
- Dashboard: clicking a node number (status table, Network list) or anywhere on a Favorites row opens a popup with the same Connect, Disconnect, Monitor and Local Mon buttons and options as the Connect bar, prefilled with that node (with the local node selector when you have several). The popup closes after a successful action. Double-click-to-connect on Favorites rows is removed.
- DVSwitch panel: "Link node" is now a green button and "Unlink node" a red one.

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- DVSwitch: after disconnecting a mode's target (for example DMR from its talkgroup), switching to another mode and back reconnected the old target. A disconnect is now remembered; a new tune is remembered as before.

## [1.2.0] - 2026-11-18

### Added
- DVSwitch support (digital voice modes DMR, YSF, P25 and NXDN through Analog_Bridge / MMDVM_Bridge). The whole feature is built around the bridge node, a private USRP node that links to one of your local nodes.
- DVSwitch tab (`#/dvswitch`, between Audio Tuning and Settings, admin only). It detects whether DVSwitch is installed (by ASLCP or by hand), shows the version, services and host files, and offers Install and Uninstall with a confirm dialog and a live job log. Install works on Debian bookworm and trixie (amd64, arm64, armhf), uses a fixed package set with a pinned repository key, and refuses to run when the clock is unset or an apt job is running. Uninstall is offered only for installs made by ASLCP, reverts the bridge node it added and restarts Asterisk.
- DVSwitch setup: identity (callsign, DMR ID, ESSID, optional NXDN ID), DMR master address, port and password (write-only, never shown; "Clear password" for masters without one), which modes are enabled, and a startup card per mode (DMR talkgroup, slot and colour code; YSF, P25 and NXDN reflector or talkgroup). Picking a startup mode opens its tab. Searchable pickers for the DMR master, YSF reflectors and P25/NXDN talkgroups. Every save is a reviewed change with a per-file history and a rollback button, and nothing restarts by itself; the page lists which services need a restart and warns that a restart resets the running mode and talkgroup to the startup values. After saving credentials, the DMR login to the master is checked and reported.
- Dashboard DVSwitch panel (after the Connect bar): current mode, target and slot with a coloured status line (amber when a service is down or the state is unknown, green when all services run and a target is tuned). Operators and admins switch mode, tune a talkgroup or reflector, and unlink; viewers see the status only. Commands run one at a time and the panel is locked until the bridge confirms.
- DVSwitch favourites: save the current target with a name (up to 40 characters, 50 per mode) and tune it with one click from chips below the panel; rename and delete from the chip menu. Favourites are shared by the whole node.
- Remembered talkgroup per mode: switching back to a mode re-tunes the target last used in it, so a YSF reflector never leaks into DMR as a talkgroup. Optional "Resume last tune after restart" (admin, off by default) returns the node to the last mode and target after an Analog_Bridge restart or a reboot.
- The whole DVSwitch panel turns light green while the bridge node is linked to your local node.
- Bridge node setup (admin): add the private node with its number, USRP ports, local node and link mode. The apply shows a diff of `rpt.conf`, `modules.conf` and `Analog_Bridge.ini` first, restarts Asterisk once and checks the bridge; if it fails, "Roll back" restores the previous files. The node is written neutral, with no ID, telemetry, courtesy tone or hang time. Numbers already in `rpt.conf` are refused.
- Bridge node: Change number and Remove, each one reviewed apply with one Asterisk restart and a rollback. Remove disconnects the link, reverts what ASLCP added and restores the previous USRP ports. Only a bridge node added by ASLCP can be changed or removed.
- Link modes for the bridge node: on-demand (Link node / Unlink node button on the panel) or permanent (ASLCP keeps the link up, also after an Asterisk restart; a manual disconnect pauses it until the next save or restart through the panel).
- "Disconnect other links first" checkbox next to the Link node button on the panel (operators and admins, on by default like the Connect bar). It applies to the Link node button and to a save of the bridge section in permanent mode; automatic relinks never disconnect anything.
- Backup: new DVSwitch part with the five `.ini` files and `dvsm.macro` (passwords included, as the download dialog warns). It is restored only when DVSwitch is installed on the target node; older ASLCP versions cannot restore such an archive.
- Demo mode and `web/mock` simulate DVSwitch; `docs/DVSWITCH.md` holds the design and the checklist for a real node.
- API: `GET /api/dvswitch`, `GET /api/dvswitch/job`, `POST /api/dvswitch/install`, `POST /api/dvswitch/uninstall` (admin); `GET /api/dvswitch/setup`, `PUT /api/dvswitch/identity`, `PUT /api/dvswitch/credentials`, `PUT /api/dvswitch/modes`, `PUT /api/dvswitch/startup`, `POST /api/dvswitch/rollback`, `POST /api/dvswitch/restart` (admin), `GET /api/dvswitch/dmr-status` and `GET /api/dvswitch/lists` (operator and admin); `GET /api/dvswitch/snapshot`, `POST /api/dvswitch/mode`, `POST /api/dvswitch/tune`, `POST /api/dvswitch/unlink`, `GET/POST /api/dvswitch/favorites`, `PUT/DELETE /api/dvswitch/favorites/{id}`, `GET/PUT /api/dvswitch/resume`, `PUT /api/dvswitch/link`; `PUT/DELETE /api/dvswitch/private-node`, `POST /api/dvswitch/private-node/rollback` (admin); SSE event `dvswitch`; `GET /api/backup/info` and `POST /api/backup` gain the `dvswitch` part.

### Changed
- Header menu: DVSwitch tab added before Settings.
- A DVSwitch install or uninstall counts as an apt job: system and ASLCP updates are refused while one runs, and the other way round.

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- Restart Asterisk: when the connection to Asterisk drops right after the restart command (an idle Asterisk restarts at once without replying), the panel now reports success instead of an error.
- Node status and the dashboard showed a node twice when Asterisk listed it twice (for example while reconnecting); each node now appears once, and an established link wins over a connecting one.
- Backup, restore and other panel actions could lose their reply with a "connection reset" error in rare cases; the helper now drains unread request bytes before closing.

## [1.1.24] - 2026-11-17

### Added
- Schedules: new setting "When I connect or disconnect manually" at the top of the Schedules tab (operators and admins; everyone sees the current choice). "Pause all until re-enabled" is the previous behaviour and stays the default. "Pause all for today" pauses the same way but switches the schedules on again by itself at the next midnight (node time); the Schedules tab and the dashboard card say "Paused until 00:00" / "Paused until midnight". "Cancel the running window, keep schedules on" only cancels the window that is running on the node you connected or disconnected on; schedules stay on, other nodes are untouched, and a notice such as "Tuesday net" cancelled by manual connect 41234 on 2006 by op is shown until the window would have ended.
- API: `PUT /api/schedules/settings` (`manualAction`); the schedules snapshot gains `manualAction`, `resumeAt` and `lastCancel`.

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- Schedules: saving a schedule that overlaps another one on the same node is now refused when one of the two has "disconnect others first" on (both connects, both enabled). Before, a new exclusive schedule saved while another window was running connected at once and dropped the running link. The dialog shows the clash (for example `overlaps "Tuesday net" (Tue 19:00–20:30) on node 2006`) and disables Save; switching a schedule on shows the same message when it would clash. API: `409 schedule_conflict`. Schedules saved earlier are left alone.

## [1.1.23] - 2026-11-17

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- Favorites scan showed a node that was transmitting continuously green (active) instead of red (keyed), because the stats service publishes a new report only about every 30 s; linked favorites now also turn red immediately from the local link, and a node that stops reporting no longer stays red.

## [1.1.22] - 2026-11-17

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- Updates tab: the System packages list missed packages whose line in apt's simulated upgrade ends with an extra `[]` (seen with Raspberry Pi mesa updates), so it could show fewer packages than `apt list --upgradable`.

## [1.1.21] - 2026-11-17

### Added
- Schedules: scheduled connects and disconnects, for example a weekly net. New Schedules tab (`#/schedules`, visible to all roles, editing for operators and admins) and a one-line dashboard card between the connect bar and the favorites with the on/off switch, the next event with a countdown ("Running: ... until 20:30" during a window) and the pause reason.
- A schedule has a local node, a target, an action (connect or disconnect), the mode, "permanent" and "disconnect others first" like the manual Connect, weekly days or a single date, a start time and, for connects, an optional end time: the link is disconnected at the end (also when it was already linked at the start); an end before the start crosses midnight. A one-off schedule switches itself off after its last event.
- Times are node time (the time zone of the machine, DST included; a time skipped by the clock change runs at the first valid minute). The Schedules tab shows the zone name and notes it when the browser is in another zone.
- A manual connect, disconnect or disconnect all from the panel (any user) cancels the windows running at that moment and pauses all schedules, with the reason shown ("manual connect 41234 on 2006 by op"); only an operator switching schedules on again resumes them, and the cancelled windows are not caught up. Changes made outside the panel (DTMF, Allmon3, a link dropping) are not detected.
- Catch-up: when ASLCP starts or schedules are switched on, a window that is running now is connected now, and a window whose end passed while ASLCP was down is disconnected. Single events are never replayed. A failed command (Asterisk down, error) is retried every minute until the end of the window, or for 10 minutes for a single event; the result is shown per schedule (`ok`, `skipped: ...`, `error: ...`).
- Backups include schedules; the restore page shows how many; a window running at backup time is not acted on after a restore.
- API: `GET/POST /api/schedules`, `PUT/DELETE /api/schedules/{id}`, `PUT /api/schedules/enabled` (operator for writes), SSE event `schedules` with the snapshot, new `schedules` table (database migration 4) and `docs/SCHEDULES.md`. Demo mode (two example schedules on node 2006) and `web/mock` simulate the feature.
- Tests: the occurrence calculation (midnight crossing, one-off dates, DST gap and repeated hour, cancelled occurrences), the engine with a fake clock (window, skip, retries, catch-up, pause, restart, ordering, one-off), time zone name detection, the store, the REST endpoints with roles and validation, manual actions pausing, and an end-to-end run against the fake node.

## [1.1.20] - 2026-11-17

### Changed
- ASLCP is now open source under the GNU GPL v3.0 or later; source code at https://github.com/artw1/aslcp.
- The apt repository moved to the gh-pages branch of the same repository; the URL https://artw1.github.io/aslcp is unchanged, no action needed.

## [1.1.19] - 2026-11-17

### Added
- Config editor: find and replace. Press Ctrl+F (Cmd+F on a Mac) in the editor, or use the new Find button above it. Supports next / previous, match case, regular expressions, whole word, replace and replace all; other occurrences of the selected text are highlighted.

## [1.1.18] - 2026-11-17

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- Restart ASLCP (and Reboot) no longer leave the "Restarting…" banner spinning. The page reloads as soon as the restarted panel answers, even when the panel is back before the restart request returns (the server now reports a per-process instance id in the `X-ASLCP-Instance` header and the SSE `hello` event). A panel that has not restarted after 60 seconds gives an error instead of spinning.

## [1.1.17] - 2026-11-16

### Changed
- Header menu order: Dashboard, Lookup, Config, Audio Tuning, Settings, Backup, Updates.
- Backup (with restore) is its own admin-only tab `#/backup`, moved out of Settings.
- Settings: About and Node list are now two cards side by side at the top (one column on narrow screens).
- "Disconnect before Connect" / "Disconnect before Monitor" reset to on / off every time the panel loads. Changing them in the connect bar lasts only until the next reload (it is no longer saved in the browser), so cross-linking is never left on by accident. The server no longer stores or serves these defaults.

### Removed
- Settings: the Updates card (the Updates tab stays) and the admin "Connecting" section.
- `PUT /api/settings` and the `exclusiveDefault` / `monitorExclusiveDefault` fields of `GET /api/settings` and the SSE `hello` settings.

## [1.1.16] - 2026-11-16

### Changed
- Version scheme: the `X.Y.Z-beta.N` tags and `X.Y.Z~beta.N` package versions are gone. Stable stays 1.0.0 and the former 2.0.0 betas continue as plain 1.1.x versions. Every `vX.Y.Z` tag now builds and publishes to the beta apt suite (and `latest-beta.json`) and creates a GitHub pre-release.
- A release is made by the new **Promote** workflow (Actions > Promote, input: version): the exact debs already published to beta move to the stable suite, `latest.json` is updated and the GitHub release becomes the latest full release. No rebuild; promoting a version that is not in beta is refused, and running it again changes nothing. Backport tags on `release/*` follow the same rule.
- New **Remove from beta** workflow (dry run by default) to delete beta packages by version glob from the apt repository.
- Settings > Updates text for the channel selector: "Beta gets new versions as soon as they are built and may be less stable. Release only gets versions promoted after testing."

## [1.1.15] - 2026-11-16

> 1.1.1–1.1.15 were published as 2.0.0-beta.1–15.

### Added
- Updates tab (`#/updates`, admin only) with two sections: ASLCP (the version, channel, check and update controls and the job log, moved here from Settings, which now has a link) and System packages.
- System package (apt) updates. The helper has new fixed ops (no arguments, no package names from the web): `os-check` (`apt-get update`, quiet and noninteractive, 5 minute timeout, then a simulated `apt-get upgrade` that lists upgradable packages with a security flag when the origin contains "security", the kept-back packages and the reboot-required state), `os-status`, `os-apply`, `reboot` and `aslcp-restart`. A held apt lock gives "apt is busy, try again later" (`409 apt_busy`).
- The server checks once a day (first check about 5 minutes after start if the last is older than 24 hours); the result and its time are stored, so restarts do not re-check. "Check now" runs a manual check (`POST /api/updates/os/check`, `202`, poll `GET /api/updates`). The ASLCP 6 hour check is unchanged.
- Installing runs a transient systemd unit with plain `apt-get upgrade` (never `full-upgrade`; `--force-confdef --force-confold`) after a confirmation that lists the packages and warns that Asterisk packages drop all links and that `aslcp` restarts the panel. Kept-back packages are shown. Only one apt job runs at a time, shared with the ASLCP update (`409 update_running`). The list is checked again automatically afterwards. Audited as `updates.os.apply`.
- Reboot: when a reboot is required (`/var/run/reboot-required`) the Updates tab says so, with a "Reboot now" button (`POST /api/system/reboot`, audited as `system.reboot`).
- Header: a compact badge with the number of updates links to the Updates tab, whose nav item shows a count in the accent colour (warning colour for security updates or a pending reboot). Admins only; new admin-only SSE event `updates` carries counts only, never package names.
- Dashboard footer: "Restart ASLCP" (`POST /api/system/restart-aslcp`, helper `systemctl --no-block restart aslcp`, audited as `aslcp.restart`), "Restart Asterisk" and "Reboot" (the same helper op as the Updates tab), all admin only, each with a confirmation. The page shows "Restarting…" or "Rebooting… waiting for the node" and reloads once the server answers again.
- `GET /api/updates` gains an `os` object. Demo mode and `web/mock` simulate OS updates (a security package, an asterisk package, a reboot-required toggle and a job log).
- Tests: the apt output parser, the daily scheduler (persisted, no re-check after a restart within 24 hours, manual check), concurrency (`409`), admin-only access for all new endpoints (`403` for operator and viewer), the audit records, and the SSE counts not reaching non-admins.

### Changed
- The "Update available" badge in the header is now the updates badge (ASLCP plus system packages) and links to `#/updates` instead of Settings.

## [1.1.14] - 2026-11-16

### Changed
- Favorites scan: the row marked is now the next one to be checked, not the one being checked. A request takes a fraction of a second on real hardware, so the ring around the # number only flashed. The # number of the next favorite now has a blinking underscore beneath it ("Next to check"; a static underline with reduced motion). The ring is gone.
- `GET /api/favorites/stats` and the `favstats` event carry a new `next` field: the node that will be requested next, chosen by the same ordering the poller uses (never checked first, then the oldest; nodes in their skip period, your own nodes and out-of-range numbers are passed over). It is empty when the scan is off, nobody is watching, nothing is eligible or every node is in its skip period. It is sent whenever it changes, including when favorites are added, removed or reordered. `checking` stays in the payload for compatibility.

## [1.1.13] - 2026-11-16

### Changed
- Favorites scan: requests are now evenly paced. The burst of 2 is gone, so the first requests are no longer 3 s apart and the rest 6 s: one request goes out exactly every 60/rate seconds from the very first, never closer than 3 s. The default rate is back to 20 per minute (one request every 3 s; `[favstats] rate` stays 1..25 as the ceiling and the rate still halves after an HTTP 429): the earlier 429s were most likely the offline-node errors fixed in beta.12, which the header mislabelled as rate limiting. The adaptive rate, backoff and `Retry-After` work as before. The request log line shows `gap=` instead of `tokens=`.
- The Favorites header only says "rate limited, retrying in Ns" for an HTTP 429. Other pauses say "stats service error, retrying in Ns" (HTTP 5xx) or "paused after a network error, retrying in Ns"; the payload has a new `backoffReason` (`rate-limit`, `service-error`, `network-error`).
- Favorites header: instead of "updated Ns ago" (which only moved on successful polls and so kept climbing, and never moved for offline nodes) it shows `checked 12 of 30 · next in 4s`, `rate limited, retrying in Ns`, or `no favorites to check`. The `updated` field stays in the API for compatibility.
- The `favstats` SSE event is sent when a request starts and when it ends instead of on value changes plus a 15 s heartbeat; `stats` still carries only changed nodes.

### Added
- Favorites scan progress in `GET /api/favorites/stats` and the `favstats` event: `checking` (node being requested), `nextAt`, `checked`, `total` (eligible favorites; `checked` counts those answered in the current pass and restarts when a pass completes) and `checkedAt` (last attempt per node, including offline ones).
- The favorite being checked right now has a ring around its # number ("Checking now"; static with reduced motion).
- Rx% tooltips show how long ago the node was checked ("checked 12s ago", "not checked yet"); offline nodes show "—" with "No data (offline)"; EchoLink and private numbers say "Not scanned (EchoLink or private node)".
- Demo mode and `web/mock` simulate the progress, the indicator and offline nodes.

## [1.1.12] - 2026-11-16

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- Favorites scan: one bad node no longer pauses the whole scan. On hardware the journal showed only `favstats: request failed (error), pausing 15s`. Responses are now decoded tolerantly (every field may be a string, number, bool or null, `links` an array of strings or numbers, `node` an object, null, array or missing; a field of the wrong type is left zero) and large unneeded parts such as `linkedNodes` are skipped token by token without being buffered. The size cap is 4 MB (was 512 KB) and an oversized response is reported as such instead of as a decode error.
- Offline and unknown nodes (the service answers 200 with `"stats":null`, an empty body, `null`, `{}`, an error object or an HTML page) no longer pause the scan or log an error: the node shows no values, is asked again after 10 minutes and goes to the back of the queue. Only the per-request line (`[favstats] log = true`, `status=200-nodata`) mentions it.
- Favorites scan: a node counts as Active (green) only while the service has live data for it. The registration record says "Active" even for offline nodes; `stats.data.time` older than 15 minutes also means offline, and so does WebTransceiver.
- Errors are classified. Network errors, timeouts, HTTP 429 and 5xx still pause the scan (backoff, adaptive rate). Invalid JSON, too-large responses and other HTTP 4xx (such as 404) only skip that node for 10 minutes and leave the success streak alone. The journal names the node and a short sanitized reason, for example `favstats: node 50001: response too large (>4 MB), skipping for 10m0s` or `favstats: request failed (timeout), pausing 15s`; the request log shows `status=too-large`, `decode-error` and `200-nodata`. Read timeouts while receiving the body are reported as `timeout`.
- Favorites scan: an open dashboard now clears a favorite's Rx%, LCnt and colour when the node goes offline, returns no data or 404. The `favstats` SSE event carries the cleared nodes in a new `removed` list instead of leaving the old values until the next full snapshot.

## [1.1.11] - 2026-11-16

### Changed
- Favorites scan: EchoLink favorites (7-digit numbers 3000000..3999999) are no longer queried and show no activity values. Only node numbers 2000..999999 are scanned.
- Favorites scan stays under the stats service's real rate limit: the default is now 10 requests per minute (`[favstats] rate`, 1..25, now a ceiling), burst 2, at least 3 s between requests. After an HTTP 429 the rate in use is halved (not below 3 per minute) in addition to the backoff, and it rises by 1 per minute after every 20 successful requests. `Retry-After` is honoured.
- Favorites scan polls nodes never queried first, then the one queried longest ago, so a backoff no longer starves the end of the list.
- The Favorites "Scan" checkbox is now a prominent "Start scan" / "Scanning" toggle button (viewers see a "Scan on" / "Scan off" badge). During a backoff the header shows "rate limited, retrying in Ns".
- `[favstats] log = true` (default false) writes one journal line per stats request (node, status, duration, rate, tokens, Retry-After) and each rate change, to diagnose rate limiting. No response data is logged.

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- Favorites scan: a 429 answer no longer counts as a sample; the last values and the "updated" time stay.

## [1.1.10] - 2026-11-16

### Added
- Favorites activity scan (as in AllScan). A "Scan" checkbox in the Favorites header (operators and admins; off by default) shows two new sortable columns, Rx% (lifetime TX time as a percentage of uptime) and LCnt (number of links), and colours each row's # cell: red while the node is keyed, green when it is Active, brighter green with WebTransceiver. The Node cell is highlighted when the favorite is linked to one of your nodes. The header shows when the values were last updated. One server-side poller queries `stats.allstarlink.org` for the favorite nodes (not EchoLink or your own), one at a time, at most 20 requests per minute and only while someone is logged in with the panel open, with backoff after rate limiting. New `GET /api/favorites/stats`, `PUT /api/favorites/scan` and SSE event `favstats` (see `docs/API.md`); never on the public page. `[favstats]` in `aslcp.conf` overrides the URL and the request rate (`docs/FAVSCAN.md`).

## [1.1.9] - 2026-11-16

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- The dashboard picks up nodes added to or removed from `rpt.conf` without restarting aslcp. The local node list is re-read when the Asterisk connection comes back, shortly after a reload or restart the panel triggers (config apply or restore with reload, Asterisk restart), and every 5 minutes. A removed node's row disappears from the dashboard and the public view; an added node appears and is polled at once.
- A stale node shows the actual reason (for example the node poll error) instead of always "AMI disconnected".

## [1.1.8] - 2026-11-16

### Removed
- Audio Tuning: the TX test tone. `susb tune flash` gave inconsistent tones on real hardware (see `docs/SIMPLEUSB_TUNE.md`). Removed the "Send test tone" button and its confirmation dialog, the `POST /api/radio/{dev}/tone` endpoint and its `radio.tone` audit entry, the tone rate limits and error codes (`node_keyed`, `channel_not_ready`, `tone_busy`, `rate_limited`), the channel-up pre-flight check, and the demo and mock tone code. The page now points to `simpleusb-tune-menu` in a terminal for a steady test tone.
- The separate AMI connection for radio tuning; it existed only so the blocking tone would not stall the shared connection. Tuning commands now use the shared one.

## [1.1.7] - 2026-11-16

### Fixed
- First-run restore: the "Log in" button after a restore opens the login form instead of the public page.
- Audio Tuning: the TX test tone was always refused with "the channel SimpleUSB/<dev> is not up". The pre-flight check expected a `-<suffix>` on the channel name, but chan_simpleusb names its channel `SimpleUSB/<dev>` with none.

## [1.1.6] - 2026-11-16

SimpleUSB audio tuning (admin only). See `docs/SIMPLEUSB_TUNE.md`.

- New "Audio Tuning" tab (`#/tune`), shown to admins when a node has a SimpleUSB device: live receive meter, RX/TX level sliders, toggles, carrier/CTCSS source, PTT mode, RX-on/TX-off delays in milliseconds, echo mode (auto-off, banner), TX test tone with confirmation, Save and Revert.
- Link guard: shows linked nodes, warns before echo and tone, names the nodes in the tone confirmation, one-click "Disconnect all links".
- Save records `simpleusb.conf` in the config history (new helper op `config-record`). Unsaved changes are lost when the module reloads or Asterisk restarts.
- New admin-only API `/api/radio` (documented in `docs/API.md`), demo mode and mock support.
- Needs hardware testing: tone and echo over AMI, save formatting. See the unverified list in `docs/SIMPLEUSB_TUNE.md`.

## [1.1.5] - 2026-11-16

Fixes restore on a real node.

- Fix: restore on a real node rejected every backup as "not a gzip-compressed backup file" (the request's trailing newline was read as part of the archive).
- Restore errors now show the server's reason.

## [1.1.4] - 2026-11-16

Debian 13 "Trixie" support, alongside Debian 12 "Bookworm" (ASL3 is supported on both).

- The installer and the documentation now cover Debian 12 (bookworm) and Debian 13 (trixie) and Raspberry Pi OS; the installer only warns on other releases.
- Backups record the Debian release of the node (`osId`, `osCodename`, `osName` in the manifest; format unchanged, older backups still restore). The restore preview shows it next to the ASL3 and Asterisk versions.
- Restoring a backup made on another Debian release (for example bookworm onto trixie) shows a warning: your files replace the new defaults, the new defaults are kept as `.dpkg-dist`, and if Asterisk or app_rpt does not start you should compare them.
- The health check after a restore now checks that the `app_rpt` module is loaded, and a node only counts as answering when app_rpt really responds.

## [1.1.3] - 2026-11-15

Backup and full restore. Download one file from a working node, re-image the SD card, install ASLCP, upload the file, and get the same node back. Try a restore on a spare SD card first.

- Settings > Backup (admins only): tick the optional parts and Download. The file is `aslcp-backup-<hostname>-<date>.tar.gz`.
- Always included: everything in `/etc/asterisk` (also `allmon.ini.php`, `custom/`, ...) with owners and permissions, AstDB, the ASLCP database (users, favorites, settings, public page, audit log) and `/etc/aslcp/aslcp.conf`, the update channel, and the SA818 radio settings in `/etc/sa818.conf` when present. Optional: config history, Allmon3 configuration and custom sounds.
- **The backup is not encrypted.** It contains every secret of the node (node, IAX, AMI and EchoLink passwords, ASLCP password hashes). Store it like a password.
- Restore from Settings > Backup (password re-entry) or on the first-run page ("Restore from a backup", with the one-time setup code, instead of creating an administrator). The file is checked completely first, then a preview shows what will happen to every file, the users that will exist, the SA818 settings, packages that are missing here (with the `apt install` command) and what to set up separately (network, hostname, timezone, SSH keys, other software). Nothing changes until you confirm.
- Restoring onto a newer ASL3 is safe: your changed and local files win, files that are still stock keep the new default, and when ASL3 changed the default of a file you edited, the new one is kept beside yours as `<file>.dpkg-dist` for review. Files only the new image has are kept, nothing is deleted unless you tick it.
- Before anything is written the current `/etc/asterisk` is saved to the config history, so a restore can be undone from History. If a step fails the changes are undone.
- Asterisk is stopped and started, ASLCP restarts itself and the page shows the health check and the "still to do" list. Everyone has to log in again with their old account.
- SA818 modules keep their frequency and tone settings in their own memory, and these survive a re-image. The restore result offers `sudo sa818-menu --apply` to program a different module from the restored file; it is not run automatically.
- A backup made by a newer ASLCP is refused ("update ASLCP first"); a different ASL3 or architecture only gives a warning.

## [1.1.2] - 2026-11-15

- Config editor: lines starting with `;---` (separator lines, as in `usbradio.conf`) no longer turn the rest of the file grey. Like Asterisk, only `;--` followed by something other than a dash opens a block comment.

## [1.1.1] - 2026-11-15

First 2.0 test version (Beta channel). Configuration editing, part 1. Start with harmless edits (comments) and keep an eye on the result page.

- New Config page (admins only): the Asterisk configuration files in `/etc/asterisk`, grouped (Core, Radio, Linking, Platform, Custom), with a code editor.
- Saving shows a review first: syntax check, the exact changes, and which reload will run. Apply takes a snapshot, writes the files keeping their owner and permissions, reloads only what changed (for example `module reload app_rpt.so` for `rpt.conf`, `iax2 reload` for `iax.conf`), and checks that Asterisk and every node still answer. If the check fails, one click rolls back.
- A file changed on disk while you were editing (by `nano`, `asl3-menu`, ...) is never overwritten: the page says so and lets you load the new version while keeping your text to copy from.
- History: every change is kept in a local git history (`/var/lib/aslcp-config-history`, root only, never sent anywhere), with diffs (passwords and secrets hidden unless you reveal them) and Restore.
- Restart Asterisk button on the Config page.
- Config changes, restores, secret reveals and Asterisk restarts are recorded in an audit log.
- The package now depends on `git`.

## [1.0.0] - 2026-11-15

First stable release. 1.0 is the stable line of everything since 0.1: live dashboard, connect and monitor, favorites, node list and lookup, public status page, users and roles, system and diagnostics, and one-click updates. It continues to receive fixes (1.0.x) while the next major version is tested as 2.0 betas.

- Update channels: Settings > Updates has a new "Update channel" switch, Release or Beta. Release (the default) only gets finished versions. Beta gets 2.0 test versions early and may be less stable. The channel is the suite in `/etc/apt/sources.list.d/aslcp.list`, so `apt upgrade` on the command line follows the same choice. Switching back from Beta to Release does not downgrade: the node stays on the installed beta until a release newer than it comes out. The install script accepts `ASLCP_CHANNEL=beta` and keeps the channel of an existing install when run again.

## [0.2.5] - 2026-11-15

- Readable text on keyed and receiving rows: node descriptions and locations on red (transmitting/keyed) and green (local receive) rows are now near-white in both themes, on the dashboard, the public page and favorites. They were grey and hard to read, especially in light mode. The light-theme receive green is slightly darker.
- Settings page sections are in a fixed order: Updates and About, then Connecting and Node list, then Public page, then Change password and Users (one column on phones).
- Updates no longer log "Ignoring unknown escape sequences" for the upgrade job in the system journal.

## [0.2.4] - 2026-11-15

- The Updates page no longer fills the system journal with "Failed to open /run/systemd/transient/aslcp-upgrade.service: No such file or directory" messages every few seconds. The helper now checks whether the upgrade job exists before asking systemd about it.
- This is the first update that is installed by 0.2.3's non-blocking updater, so it should finish without the "systemd-run failed" or "unexpected end of JSON input" message and without the pause near the end.

## [0.2.3] - 2026-11-16

- Fixed the real cause of "helper: bad response: unexpected end of JSON input" when updating: the helper waited for the whole upgrade before answering the Update request, and the package script, which restarts the helper, then waited for the helper. The helper now starts the upgrade and answers at once, which also removes a pause of about 20 seconds near the end of every upgrade. (This takes effect from the update after 0.2.3, because the updater that runs is the one already installed.)
- Instant update checks: "Check now" and the background check (1 minute after start, then every 6 hours) no longer run apt. They fetch the small `latest.json` from the update URL and compare versions themselves, so the check takes a moment instead of many seconds on a Raspberry Pi 3. Manual checks bypass the CDN cache.
- Cheaper status polling: the update page and its polling no longer load apt's package cache; they ask only for the installed version and the state of the upgrade job (the journal is read only while an upgrade runs). The page shows the last check time ("just now", "5 min ago") and keeps the check working, with only the Update button disabled, when the helper is not running.
- Faster upgrade: one step (refresh the ASLCP apt source and install), no changelog reader, no recommended packages, and the package script skips the firewalld reload when the rule is already open and restarts ASLCP last so the web interface returns sooner. If the package index lags behind the release information the page says "Already up to date" instead of showing an empty success.

## [0.2.2] - 2026-11-16

- The helper finishes requests in flight when it is restarted during an update; the update page no longer shows "unexpected end of JSON".
- The update page rides out the helper restart.

## [0.2.1] - 2026-11-16

- Settings > Updates no longer stays on "An update is running…" after an update. The page reloads into the new version while the upgrade is still finishing; it now keeps checking until the job ends instead of showing the first answer forever.

## [0.2.0] - 2026-11-16

- Public status page (optional, off by default; Settings > Public page): a modern, read-only replacement for public Allmon3/Supermon pages. When enabled, visitors who are not logged in see a live status view at the panel's address (keying, connected links, optional linked-network list and recent activity) with a "Log in" button; logged-in users still get the dashboard and can preview it ("Public view"). It never shows IP addresses, system information, favorites or users; you choose the nodes and whether callsigns, network list and activity are visible.
- It runs on the same server and port (public data at `GET /api/public/status` and `/api/public/events`, 404 while disabled; the rest of `/api` still requires a login). Visitors are rate-limited (429), with per-address and global caps on live connections; a "trust proxy headers" setting makes the limits use the real address behind a tunnel or reverse proxy.
- Optional embedding: `/embed` serves a chrome-less view that can be put in an iframe when "allow embedding" is on. Every other page, the login page included, keeps `frame-ancestors 'none'`.
- Recent activity: the server now records transmissions (link keyups and local receiver keyups, 7 days, at most 10,000) in a new `activity` table; `GET /api/activity` lists them for logged-in users.

## [0.1.2] - 2026-11-15

- New public home at https://github.com/artw1/aslcp (the apt repository is now https://artw1.github.io/aslcp). Existing installs are migrated automatically on upgrade (apt source and an unmodified `[updates] url` line); the packaged `aslcp.conf` no longer sets URLs, so the built-in defaults apply.
- Settings > About: versions, platform, and links to report a bug (prefilled issue form), request a feature and release notes; administrators can download a diagnostics file with private data (IP addresses, secrets, emails, other stations' callsigns) removed. A small "Report a bug" link was added to the footer.
- The package now ships a copyright file with the licence.

## [0.1.1] - 2026-11-15

- The install script now asks for the admin username and password right in the terminal (no echo, confirmed twice). Without a terminal (for example a non-interactive install) it prints a one-time setup link instead.
- New `aslcp admin` commands on the server: `create`, `reset-password USER` (also signs that user out everywhere) and `list`, for creating the first account or recovering a lost password. They work on the database directly and are safe while the service runs.
- The one-time setup link (`/#/setup?code=...`) is logged at startup, so `journalctl -u aslcp` shows a clickable link; the setup page fills in the code from the link.
- The service no longer blocks resource syscalls the Go runtime uses at startup (systemd SystemCallFilter fix).
- Disconnect-before-connect: commands to a node are now paced with a minimum gap, so a connect that replaces a link no longer races the disconnect before it.

## [0.1.0] - 2026-11-15

First release (milestone 1).

- Live dashboard for local AllStarLink nodes over AMI: links, keyed state, node statistics, system strip (CPU temperature, load, memory, Asterisk status).
- Connect, disconnect and disconnect-all, with "disconnect before connect" to avoid cross-linking networks; monitor and local-monitor modes; permanent links.
- Favorites with groups and ordering, import from AllScan, node lookup by number, callsign, description or location, node list refresh.
- Users with admin, operator and viewer roles, first-run setup code, rate-limited login, CSRF protection.
- Debian packages (arm64, armhf, amd64) with a hardened systemd service, a socket-activated root helper, firewalld service definition and a signed apt repository.
- In-app updates: Settings > Updates shows the installed and available version with release notes and installs updates on request.
