# Replacing ASL3 with a native nodespot node

Research notes, 2026-11-26. Sizes and times are estimates, not measured from a prototype.

## Goal

> **Update:** the design is now *modes*. AllStar, EchoLink, BrandMeister, DMR (other Homebrew masters), YSF, P25, NXDN and M17 are equal modes, exactly one active at a time. The radio (the SHARI, or a fake radio on a radioless node) is the one fixed local end: the active mode's audio goes out to the radio and the radio's audio goes out to it. Networks are never bridged. There is no ASL3 import and no rollback to Asterisk: nodespot is a fresh install. Where the notes below say otherwise (USRP, a DV bridge, ASL3 upgrades, running beside Asterisk), the modes design wins.

Register on AllStarLink, get a node number, install nodespot, enter a few numbers and passwords, and be on AllStar and EchoLink. DV setup stays as it is today.

| Field | From |
| --- | --- |
| Callsign | License |
| AllStar node number and node password | AllStarLink portal |
| EchoLink callsign (with or without -L) and password | EchoLink account |
| Radio frequency and CTCSS/DCS | Written to the SA818; skipped for a radioless node |

**One screen, no wizards.** Those fields are the whole setup: fill them in, save, and the node registers and is on the air. Nothing else is asked.

- **Detected, not asked:** the SHARI's CM108B sound chip and its PTT and COS pins, and the SA818's serial port. No sound card or "usbinvert" choice.
- **Levels:** our AGC handles receive and network audio. At most a single TX deviation slider, set once.
- **Gone:** rpt.conf, iax.conf, extensions.conf, simpleusb.conf and the wizards that write them, plus Asterisk restarts after a change. Settings apply live.
- **Upgrades from ASL3:** a one-time import reads the existing config and fills in the same screen. New installs never see it.

**Outbound only.** The node makes connections; it does not take them. AllStar out needs no port forwarding: our node starts the IAX2 call, the router keeps the reply path open, and the far node's IP check matches because registration and the call leave from the same public IP. EchoLink out works behind most home routers the same way: before its first connect packet on the control port, the node sends its station info from the audio port, so the router already lets the station's audio in when the station starts sending (the echo test greets as soon as the connect packet arrives). The info is resent with each connect retry and once the station answers; with the "Station info" field empty it is the callsign, name and location. Incoming IAX2 calls are refused, so there is no public port to secure.

**Later, only if problems show up:** UPnP or NAT-PMP port opening, EchoLink proxy or relay fallback for strict NAT, and hints for -L/-R callsigns.

## Summary

Replacing ASL3 entirely is a large project: about 12k–21k lines of Go plus 5k–8k lines of tests for the end-user scope (outbound only, one link, EchoLink, SHARI, DV, no DTMF or hub features). For one developer that is roughly 6–10 months, or 3–5 months for a first usable node (IAX2 linking, registration and the SHARI radio, simplex). These are estimates, not measured from a prototype.

- **Biggest code piece:** IAX2 linking that interoperates with every app\_rpt node, about 3k–5k lines (client only).
- **Biggest non-code risks:** undocumented app\_rpt link frames and EchoLink details, and AllStarLink's position on non-app\_rpt nodes. Third-party clients already register in node mode, which suggests it is allowed.
- **Registration pitfall:** a node that only registers when it starts can be refused by other nodes for up to 15 minutes. A node registered continuously, as ASL3 does, is not.

**Recommendation:** keep Asterisk for IAX2 linking first and move the radio side (SimpleUSB, EchoLink) into Go over USRP, about 4k–6.5k lines. Build native registration and IAX2 on a test node in parallel, and cut over only after a long soak with Asterisk kept as the rollback.

## What nodespot uses from ASL3 today

nodespot touches many files but does little with them: about 4 AMI actions, 10 CLI strings, 1 event family, 9 config files, 2 systemd units and one UDP bridge. About 3,500 lines of nodespot Go exist only to talk to or configure Asterisk. All radio, linking, audio and EchoLink behavior lives in Asterisk.

| Area | What nodespot uses | Where |
| --- | --- | --- |
| AMI transport | Login, `Command` action, `Events: call` filter, credentials from manager.conf | `internal/ami/` |
| Status | `RptLocalNodes`, `RptStatus` (RptStat, XStat, SawStat, NodeStat), `RPT_*` events | `internal/rpt/client.go`, `events.go`, `monitor.go` |
| Control | `rpt cmd <local> ilink <code> <target>` only (codes 1, 2, 3, 6, 8, 11, 12, 13, 18) | `internal/rpt/client.go` |
| Lifecycle | `core show uptime`, `core show version`, `core restart gracefully`, per-file module reloads | `internal/rpt/`, `internal/configsvc/reload.go` |
| Config files | rpt.conf, simpleusb.conf, usbradio.conf (record only), iax.conf, echolink.conf, extensions.conf, modules.conf, manager.conf, rpt\_http\_registrations.conf | `internal/astconf/`, `internal/configsvc/` |
| Services | `asterisk` restart and journal; allmon3, apache2, cockpit only switched off | `internal/helper/` |
| Audio tuning | chan\_simpleusb's tune menu over the CLI (`susb tune menu-support`, `susb tune save`) | `internal/susb/` |
| DV bridge node | chan\_usrp over loopback UDP, a rpt.conf stanza, a permanent `ilink 13` link | `internal/dv/usrp/`, `internal/astconf/bridge.go` |
| Node list and stats | Node directory download and public stats polling; never registration, extnodes or astdb | `internal/nodelist/`, `internal/favstats/` |

nodespot never sends `rpt fun`, `rpt macro`, playback or DTMF commands. It only reads status and issues ilink, which keeps the control surface a replacement must offer very narrow. The DV bridge already sits behind a small USRP interface, so it is the easiest piece to move.

## Replacement pieces

IAX2 linking is the largest and riskiest piece; the radio side is bounded and well documented. The DV engine gives scaffolding only: the 20 ms pacer, call state machine, AGC and meters, UDP client patterns and test fakes. It has no resampler, jitter buffer, G.711, GSM, IAX2, RTP, HID or ALSA code.

| Piece | Size | Go lines | Main risk |
| --- | --- | --- | --- |
| IAX2 client, outgoing only (call token, MD5 auth, ulaw, jitter buffer, keepalives) | XL | 3,000–5,000 | Interop with every app\_rpt node; no public port to secure |
| app\_rpt link text conventions (link lists, keyup) | M | 600–1,200 | Undocumented; needs packet captures and the app\_rpt source |
| Simplex node logic (COS/PTT, timeout, hang, ID; no DTMF) | M | 1,500–2,500 | Matching app\_rpt behavior people expect |
| Radio interface for SHARI (USB sound chip audio, HID PTT/COS, resampling, AGC and levels, SA818 programming) | M–L | 2,000–3,000 | Clock drift and audio quality |
| EchoLink, required (directory login, UDP 5198/5199, GSM) | M–L | 1,800–2,800 | Directory strings and timers not documented |
| Registration and node lookup (stats posting later, opt-in) | S–M | 500–900 | Low; stats posting is opt-in and comes later |
| Radioless node and link mixer | S | 300–500 | Low: one link at a time, no hub mixing |
| DV bridge integration | S | 200–400 | Low |
| nodespot migration (new state source, config, backup) | L | 2,500–5,000 changed, 3,500 deleted | UI and API mostly kept |
| Tests, fakes, captures, soak | L | 6,000–9,000 | Realistically the larger half of the work |

Left out on purpose: remote base and autopatch. Lost unless rebuilt: the app\_rpt web transceiver, Allmon3 and Supermon compatibility (they read AMI; not needed, nodespot replaces them), and community troubleshooting knowledge.

## Middle paths

Keeping Asterisk for IAX2 linking and moving the radio side into Go is much easier, at about 4,000–6,500 lines.

- **A. Keep Asterisk for linking (recommended first).** Go owns SimpleUSB, EchoLink and the DV engine, and talks to Asterisk over USRP, the pattern the DV bridge already uses. ID, telemetry and timers stay in app\_rpt, so the Go side must send keyup and DTMF through USRP. Asterisk, AMI and the reload code stay.
- **B. The reverse (not recommended).** Replacing IAX2 but keeping the channel drivers fails because app\_rpt couples link logic with keying, mixing and ID. The Go stack would end up as another bridge node.
- **C. A sidecar IAX2 endpoint (not recommended).** It carries the full IAX2 and registration risk for little gain.

Path A also builds pieces a later IAX2 slice needs: the audio device, resampler, GSM codec, mixer, and tone and ID generation.

## Minimum native node

nodespot needs a small part of what Asterisk and app\_rpt offer: AllStar linking, EchoLink, one radio and the DV bridge. It is an end-user node with one link at a time, not a hub. Everything else stays out.

**Must have**

1. **IAX2 subset.** Call setup, call token, MD5 auth, ulaw audio, keepalives and hangup, outgoing only; incoming calls are refused. No SIP, dialplan or codec translation.
2. **app\_rpt link text.** Link lists and keyup messages, so other nodes and dashboards see who is linked and talking. Links work without them, but we would look like an empty node.
3. **Registration and lookup.** The HTTPS registration in Registration spec, plus the SRV/A lookup of the nodes we link to.
4. **EchoLink (required).** Directory login, UDP 5198/5199 and GSM. It is a mode of its own next to AllStar and DV, not an optional extra.
5. **Mixer.** One AllStar link at a time (a hub we link to still arrives as one stream), EchoLink, the radio and the DV bridge in one mix, never echoing a source back to itself. Jitter buffer, timeout timer, hang time.
6. **Radio.** The SHARI interface (see Radio hardware below); a radioless node skips it.
7. **ID, on RF only.** The rule (Part 97.119 in the US) covers radio transmissions, so the ID goes into the local transmit path after the mixer and never to the AllStar, EchoLink or DV legs; each far-end RF station IDs itself. It plays only after activity: if the radio keyed up (for any source, local or net audio) since the last ID, the node IDs within 10 minutes and once at the end. An idle node stays silent. The ID rides the tail of a transmission already in progress where possible, as fast, low-level CW (voice ID optional). On by default; the operator can turn it off, with a note that the licensee is responsible for IDing.
8. **DV bridge.** Audio goes straight into the mixer instead of through Asterisk over USRP.

**One audio path we own.** With every source in our own process, the same AGC, levels and meters the DV module uses apply to every leg: radio to network, network to radio, EchoLink and DV. No more tuning levels in chan\_simpleusb and again in the DV engine.

**Left out on purpose:** dialplan, SIP, voicemail, CDR, AMI, autopatch, remote base, macros, the scheduler, DTMF commands (none are used today; maybe later) and most telemetry voice files apart from the ID. Stats posting comes later as an opt-in setting.

**Radio hardware (SHARI).** The SHARI pairs the SA818 radio module with two USB chips:

| Part | Job | Native node |
| --- | --- | --- |
| SA818 | The radio: transmit, receive, squelch, CTCSS/DCS | Nothing at run time; it just keys and opens squelch |
| USB sound chip (CM108B on our SHARI) | Audio both ways; its GPIO pins key PTT and read COS (the SA818's squelch line) | Our audio and HID code, replacing chan\_simpleusb, with our AGC |
| USB serial (usually CH340) | Programs the SA818: frequency, tones, squelch, volume, filters | An SA818 settings page, replacing `sa818-menu` |

The SA818 cannot report its settings back, so we keep our own copy, as `/etc/sa818.conf` does today.

**Ready, station fallbacks and remembered tunes.** A mode is *ready* when every required value is set on the mode or comes from the station: the callsign (EchoLink adds `-L`), the DMR ID or the NXDN ID. An empty field uses the station value, and the UI shows it as the placeholder ("from station: N0CALL"). The echo test needs nothing and is always ready. `modes` lists each mode's `configured` (ready), `missing` (keys of the required values still absent), `saved` and `last` (the remembered target). There is no "default reflector" or "default talkgroup" setting: each mode remembers its last tune in `tune.json` next to `node.json` (`{"version":1,"modes":{"p25":{"target":{...}},"ysf":{"unlinked":true}}}`, mode 0600) and reconnects to it whenever the mode becomes active, including at startup. Only an explicit disconnect forgets it; a far-end drop or a failed connect keeps it. An old `address` setting (and `station.active.target`) seeds the remembered target once when no tune is stored, then is ignored; the old keys still parse.

**USB radio interfaces and presets.** The radio side is one USB driver with presets, not a SHARI special case. Any C-Media CM108, CM108AH, CM108B, CM109, CM119, CM119A or CM119B sound chip (USB vendor `0d8c`, the products chan\_simpleusb accepts, plus the 0x6a00 to 0x6aff range some small makers use) is detected from sysfs, with its hidraw node and ALSA card. A preset fills in the settings, and each can be overridden in the station's radio settings (`/etc/nodespot/node.json`, `station.radio`):

| Preset | PTT | COS (carrier) | CTCSS input | SA818 |
| --- | --- | --- | --- | --- |
| `shari` | GPIO3, not inverted | GPIO2 (volume down), present when high (`carrierfrom=usbinvert`) | none (`ctcssfrom=no`) | programmed by the node |
| `uri` (generic CM108/CM119 board on an external radio, URI-style or RA-style) | GPIO3, not inverted | GPIO2 (volume down), present when low (`carrierfrom=usb`) | none | no: set frequency and tone on the radio |
| `none` | no PTT | none | none | no (radioless: the fake radio, no audio) |

The `mmdvm` preset (early preview) is not a USB sound chip: it is an MMDVM hat on the Pi's UART, set in `station.radio.mmdvm` (`device`, default `/dev/serial0`; `baud`, default 115200; `duplex`, default on; `rxFreqHz`, `txFreqHz`, `rxOffsetHz`, `txOffsetHz`, `rfPower`, `colorCode` (default 1), `allowTx` (default off, forced off on restore) and the optional `resetGpio`). The pin overrides do not apply to it. With a receive frequency set, the node sets the hat up in simplex (transmit power 0) and passes DMR calls heard from a handheld on slot 2 to the active BrandMeister, TGIF or DMR network, shown as "via RF" in Now calling and Last heard. It never transmits on RF and plays nothing from the network to RF yet; a duplex hat hears nothing from a handheld until it transmits, so duplex waits for transmit. Without a receive frequency it only detects the hat and polls its status. See [MMDVM.md](MMDVM.md). To tune the receive offset against a handheld, run `sudo nodespot-node calibrate` (manual, or `-sweep`; it stops the node service while it runs and starts it again); on a Pi with no node installed, use the standalone form `sudo nodespot-node calibrate -rxfreq 433500000` (no config is read and nothing is saved; it prints the offset to enter in Settings > Station > Radio); see "Calibrate" in MMDVM.md.

The pins are the chip's four GPIOs, which the HID input byte reports as volume up (GPIO1), volume down (GPIO2), mute playback (GPIO3) and mute record (GPIO4). COS can also be `none` (never a carrier) or `always` (squelch held open). When a CTCSS input is set, the carrier needs both COS and the tone input, as simpleusb does with `ctcssfrom`. The "active low" settings are simpleusb's `invertptt` and `carrierfrom=usb`. SA818 programming, the frequency, the tone and the squelch level apply only when the interface has an SA818; otherwise they are hidden because they are set on the external radio. A config with no preset keeps today's behavior: the SHARI when one is detected (a CM108B chip, or a CH340 serial bridge beside any supported chip), otherwise none. VOX squelch is not implemented.

## Lessons from QSO One

A node that registers only when it starts can be refused by other nodes for 5–15 minutes; a continuously registered ASL3 node connects at once. QSO One in Node Mode shows exactly this: some nodes refuse it for 5–10 minutes after login, while its Web Transceiver mode and a normal ASL3 node work right away. QSO One itself says a new registration can take up to 15 minutes to reach every node.

**How a node checks an incoming link (app\_rpt source).**

- `rpt_exec()` in `apps/app_rpt.c` calls `node_lookup()`, then checks the caller's IP against the registered IP for its node number.
- Lookup order: the local `[nodes]` stanza, then DNS (`_iax._udp.<node>.nodes.allstarlink.org` SRV, then the A record), then the downloaded `rpt_extnodes` file (`apps/app_rpt/rpt_config.c`).
- A refusal logs `Node N IP x does not match link IP y!!` or `Reported node N cannot be found!!`.
- Web Transceiver calls arrive in `[allstar-public]`, are checked by token against AllStarLink's API, then run `Rpt(N,X)`. The `X` option skips the IP check, so WT is never delayed.

**Timing.**

| Source of the delay | Duration |
| --- | --- |
| Positive DNS record cache (Route 53) | 60 s |
| Negative cache of "not found" (Route 53 SOA record TTL; zone minimum is 86400) | up to 900 s (15 min) |
| ASL3 node list file refresh | 3–5 min |
| ASL2 node list refresh | about 10 min |
| HamVOIP (its own DNS) | misses reportedly cached indefinitely |

**What we observed.** While QSO One was logged in, an `asl-node-lookup` of its node number on our SHARI node found no SRV, A or TXT record and no local entry; the link was refused. The `nodes.allstarlink.org` zone is served by Amazon Route 53. Not yet measured: how long after login the record appears on the authoritative server.

**Why ASL3 has no delay.** It registers over HTTPS every minute, around the clock, from the same IP, so its DNS record always exists and always matches. Starting ASL3 creates nothing new for other nodes to learn.

## Registration design for our native node

Our node registers the way ASL3 does, continuously, so no other node ever caches it as unknown. HTTP registration is small (a few hundred lines of Go) and easy to test, because the DNS record either appears or it does not.

- **Always registered.** Register over HTTPS from service start and refresh every 60 s (ASL3's default; its shipped config uses 180 s), even with no links up. Mirror `res/res_rpt_http_registrations.c` in ASL3; the wire format is in Registration spec below.
- **One IP for everything.** Registration and IAX2 run in one process and leave from the same public address. Re-register at once when the public IP changes.
- **No links before DNS.** At startup, query our own SRV record on the authoritative Route 53 server. Until it shows our current IP, the UI shows "registering…" and outgoing links wait, so no node caches "not found" for us.
- **Keep the existing node number.** Replacing ASL3 on the same node number and IP means the record already exists and matches, so cutover adds no delay.
- **Clean handover.** Asterisk and our node cannot both register and listen on the same node number and port. Cutover stops Asterisk's registration before ours starts; rollback does the reverse.
- **Plan for change.** ASL3 is moving toward key-based authentication, which would replace the IP check.

## Registration spec

ASL3 registers with one HTTPS POST per node to `https://register.allstarlink.org/`, repeated every 60 s by default (180 s in the shipped config); the server takes our IP from the connection. Taken from `res/res_rpt_http_registrations.c` at app\_rpt commit `d881a6b`; items marked unknown are server-side and cannot be read from the client.

**Config** (`/etc/asterisk/rpt_http_registrations.conf`)

| Setting | Section | Default | Notes |
| --- | --- | --- | --- |
| `register_interval` | `[general]` | 60 s | Shipped config sets 180. Invalid value keeps 60. |
| `register => node:password@host[:port]` | `[registrations]` | none | One line per node. The optional port is the registrar's HTTPS port, not the IAX port. A password with `:` or `@` cannot be written. |

**Request**

- `POST https://<host>/` (or `https://<host>:<port>/`); path always `/`, HTTPS only, redirects not followed.
- Headers: `Content-Type: application/json`; User-Agent is Asterisk's libcurl agent string (believed `asterisk-libcurl-agent/1.0`, not verified). No auth header.
- Body, one node per request:

```json
{"port":4569,"data":{"nodes":{"1999":{"node":"1999","passwd":"secret","remote":0}}}}
```

| Field | Type | Meaning |
| --- | --- | --- |
| `port` | int, optional | IAX `bindport` from iax.conf; omitted if unknown |
| `data.nodes.<n>.node` | string | Node number |
| `data.nodes.<n>.passwd` | string | Node password, plaintext inside TLS |
| `data.nodes.<n>.remote` | int | Always 0; meaning unknown |

**Response**

- JSON with `ipaddr` (the IP the server sees), `port`, `refresh` and `data`.
- Success = the re-serialized `data` contains the substring `successfully registered`. A 2xx reply without it sets "Not Registered".
- Non-2xx, transport errors and non-JSON bodies leave the state unchanged, so ASL3 can show "Registered" after failures. We should track the last success time ourselves.

**Timing**

- Registers at startup, then every interval + 1 s measured from the start of each cycle.
- 15 s connect and total timeouts; no retry, backoff or jitter; the server's `refresh` is ignored for scheduling.
- A reload re-registers at once. Shutdown sends no unregister; the registration lapses on the server.

**IP and DNS**

- The client never sends an IP; the server uses the HTTPS source address (docs: hotspot proxies register the proxy's IP). The module does not force IPv4. Our Go client should dial `tcp4` until IPv6 is confirmed, since DNS publishes only A records.
- Docs: registrations reach `nodes.allstarlink.org` DNS "within 60 seconds"; SRV TTL 30 s, A TTL 60 s. The legacy node list is CDN cached for 300 s.
- Records stay for an unknown time after a node stops registering. ASL3's health check flags "not registered in last 10 minutes".
- Register by one method only, HTTP or IAX, never both.

**Status to mirror in our UI.** `rpt show registrations` prints Host, Username, Perceived (ip:port or `<Unregistered>`), Refresh and State. We add last attempt, last success, last error and HTTP status.

**Security.** TLS verification on, system CA store. The password sits in plaintext in the conf file and ASL3 logs it at debug level 2; we never log the body.

**Unknown (server side)**

- [ ] Exact success and failure `data` strings, and error codes for a bad password or unknown node
- [ ] Source IP only, or `X-Forwarded-For` too; IPv6 policy
- [ ] Meaning of `remote`
- [ ] Whether one request may carry several nodes
- [ ] Rate limits for intervals under 60 s or repeated bad passwords
- [ ] Registration expiry and how long DNS records persist
- [ ] Whether the server probes the IAX port

**Go sketch** (wire format only, not production code)

```go
type nodeEntry struct {
	Node   string `json:"node"`
	Passwd string `json:"passwd"`
	Remote int    `json:"remote"` // always 0
}
type regBody struct {
	Port int `json:"port,omitempty"` // IAX bindport
	Data struct {
		Nodes map[string]nodeEntry `json:"nodes"`
	} `json:"data"`
}
type regReply struct {
	IPAddr  string          `json:"ipaddr"`
	Port    int             `json:"port"`
	Refresh int             `json:"refresh"`
	Data    json.RawMessage `json:"data"`
}

// ok = bytes.Contains(reply.Data, []byte("successfully registered"))
// POST with a 15 s timeout, no redirects, tcp4 dialer; loop every interval+1 s.
```

Source: [res\_rpt\_http\_registrations.c](https://github.com/AllStarLink/app_rpt/blob/d881a6bf2e825efbb0d122985d1606892eb7fb55/res/res_rpt_http_registrations.c), [rpt\_http\_registrations.conf](https://github.com/AllStarLink/app_rpt/blob/d881a6bf2e825efbb0d122985d1606892eb7fb55/configs/rpt/rpt_http_registrations.conf), and the ASL3 manual pages on HTTP registration, IAX registration, DNS servers and the API.

## Order of work and open questions

Start with feasibility and the radio side; the native IAX2 node runs on a test node number until it has soaked for weeks.

1. **Feasibility (1–2 weeks, no node code).** Ask AllStarLink about non-app\_rpt registration and stats posting. Capture IAX2 between two ASL3 test nodes and an EchoLink session as test vectors. ~~Write the registration spec from the ASL3 source.~~ Done, see Registration spec.
2. **Path A: radio side in Go.** The SHARI radio interface and EchoLink as Go components talking USRP to Asterisk, with the DV module's AGC on every leg. Port the tune page to the native device and add SA818 programming.
3. **Native registration.** The continuous HTTPS registration and DNS check from the section above, on a test node number.
4. **IAX2 link, radioless node.** Framing, call token, MD5, ulaw, jitter buffer, keyup, link lists; the DV bridge attached through the existing USRP interface. Run beside Asterisk.
5. **Simplex node logic.** COS/PTT, timeout, hang, ID and announcements. DTMF control later, if ever needed.
6. **Parity and cutover.** Opt-in stats posting, config migration, backup and restore, rollback to Asterisk, a security review (no inbound ports), a soak of several weeks.

Keep the API and UI behind a `Node` interface backed today by `internal/rpt`, so each step ships without a UI rewrite.

**Status (branch try/native-node).** Built: the `nodespot-node` daemon (`internal/native/...`) with the radio (CM108 HID PTT/COS, ALSA audio, SA818 programming, a fake radio), the node engine (one active mode, simplex logic, hang, transmit timeouts, AGCs, ID on RF only after activity, playout prebuffer), the modes (AllStar over native IAX2 with background registration, EchoLink with GSM and the directory, and the DV networks on the existing protocol and vocoder code), the control socket, the web app and UI rebuilt around modes, the first-run setup, backups and packaging without ASL3. The echo test mode (`echotest`) with level reports and a live input meter on the Radio tune page is built. Unit tests use loopback fakes and fake clocks only.

**EchoLink receive and diagnostics.** A received talk spurt may restart its RTP sequence numbers at any value: the echo test plays a recording back with sequence numbers below those of its greeting, and other clients renumber per transmission. The connection starts sequence tracking afresh with each spurt (after the 400 ms talk hang) and when we start transmitting to the station (the playback after it is a new stream even if it comes within the talk hang), and, within a spurt, drops only a duplicate or a packet up to 15 behind the last one, as late. Before this, a duplicate or a packet up to 999 behind was dropped as bad for the whole connection, so the echo test's playback never reached the radio and nothing was logged. The receive queues hold five seconds in the connection and thirty in the mode, so a station that sends faster than real time (a parrot playing back in bursts, or Wi-Fi bunching packets) is queued and played at the radio's pace. Before, the mode's queue held one second and silently dropped its oldest frames when full, so a fast playback came back with most of it missing and no counter showing it. Every place audio can be lost on the way to the radio is counted: the Modes card shows the link counters (`rx N pkts · tx N pkts`, plus late, dropped, bad, send errors and not played when non-zero; status `link` with `proto: "echolink"`). `dropped` counts frames lost because a receive queue was full, in the connection or the mode; `not played` (node status `radio.netDropped`, for every mode, restarting when the mode changes) counts network frames thrown away because a local station had the receiver or the transmit timeout had locked the network out. The log has `echolink: sending` and `echolink: sent N packets` for each local transmission, `echolink: received N packets (late L, dropped D)` at the end of each received one (D includes the mode queue's drops), and with `-debug` a summary line at the start and end of each spurt each way (addresses, sequence numbers, counts; never audio; a received spurt's start line has `since_call`, the time since we called the station). While the radio's carrier is passed to the active mode, the node status has `radio.rxActive` and the Dashboard and Modes card show "Local station · sending to <mode>", for every mode. The node publishes an `rx` event when it changes; before, the web server took `rxActive` only from full status reads, which a carrier change did not trigger, so "Local station" rarely appeared. `internal/native/node/echolink_e2e_test.go` runs the node with the fake radio against a fake directory and parrot over localhost UDP (skipped with `-short`): a ten-second transmission played back in real time and four times faster, a pause mid-playback, a transmission right after a greeting numbered above the playback, a short carrier drop and the default key-up delay; each checks that the parrot got all we sent, that all of its playback came out of the radio, the talker and the counters. `TestEchoLinkGreetingStart` has the parrot greet as soon as our connect packet arrives, answering at once or late, with and without a simulated router that drops audio until we have sent from our audio port, and checks that the greeting's louder first half second comes out of the radio whole.

**BrandMeister Parrot and diagnostics.** The BrandMeister mode (`internal/dv/network/bm`, Rewind as an Open DMR Terminal on port 54006) tunes a talkgroup with an empty CANCELLING and a SUBSCRIPTION `{7, TG}` (group voice). The Parrot (9990) and other private targets subscribe `{5, own DMR ID}` (private voice) instead: the Parrot's playback is a private call from 9990 to your DMR ID, and the master delivers a private call only to a terminal subscribed for its destination ID. Before 1.0.0 the Parrot was subscribed as `{7, 9990}` and never answered. A transmission is three voice headers (Full LC: FLCO 0x03 for a private call, 0x00 for a group, destination, source and the masked RS(12,9) parity), 27 byte audio packets of three AMBE+2 frames, and a terminator, all with the real-time flag 1; no super header is sent. With the node's `-debug` flag the log has `bm: logged in` (or `bm: login failed` with the reason), `bm: subscription sent` (session, type, number, target), `bm: tx start` and `bm: tx end` (session, dst, src, audio packets), `bm: rx start` and `bm: rx end` (session, src, dst, packets, frames, why it ended), `bm: master closed the session`, and `bm: unexpected packet from the master` (type, length, the text of a report or failure code; at most once a minute per type). For a Parrot test the lines should read `subscription sent session=private type=5 number=<your ID> target=9990`, `tx start session=private dst=9990`, `tx end ... packets=N` with N > 0, then a second or so later `rx start session=private src=9990 dst=<your ID>` and its `rx end`. No `rx start` after a `tx end` means the master sent nothing back; a failure code line says why when it sends one.

Untested on real hardware or networks: registration against AllStarLink and the DNS check; IAX2 calls to real app\_rpt nodes (call token, MD5, keying, link lists, disconnect and hangup handling); EchoLink directory login and QSOs behind a home router; each DV network's login and audio (and the software AMBE vocoder); the echo test on a real radio (levels and the warning thresholds); CM108 PTT/COS on a real SHARI, ALSA timing and levels, SA818 programming; the ID on air; the self-restart after a radio wiring change; the .deb install on a fresh OS.

**Morning test.**

1. Flash a fresh OS on the node.
2. Install the test .deb from the CI run page.
3. Open the app: the setup opens.
4. Fill in the station (callsign, frequency and CTCSS/DCS, ID) and one mode, then save.
5. Connect to a target in that mode and check you hear it on the radio.
6. Key the radio and check the far side hears you.
7. Watch the ID: it should go out on RF after the activity, not before.
8. Switch to another mode: the first one drops, the new one connects.
9. Echo test for levels: on Settings > Radio tune, press "Switch to echo test", key the radio and talk for a few seconds. About a second after you unkey the node plays the recording back. Check the live meter and the Echo test card: peaks should sit in the shaded band (-20 to -6 dBFS) with no warnings. "clipping" means a peak above -0.5 dBFS or any full-scale sample; "too quiet" means voiced RMS under -30 dBFS or no voiced audio (frames below -50 dBFS are ignored). Pick a Codec in the Echo test card on the Modes page (next to Start echo test) to hear how a network codec sounds; it is saved at once and the next recording uses it.

**Open questions**

- [x] Answered: no DTMF functions or macros are used from RF. DTMF control can come later.
- [x] Answered: public maps and stats are optional, an opt-in setting (off by default).
- [x] Answered: either works. The protocol is the same; the callsign, with or without -L, is just a setting.
- [x] Answered: one. This is an end-user node, not a hub.
- [x] Answered: our SHARI's sound chip is a CM108B (USB ID 0d8c:0012, already on chan\_simpleusb's supported list). Pins are stock: COS from the chip's inverted input (carrierfrom = usbinvert), CTCSS input off, PTT not inverted.
- [x] Answered: no. nodespot does not use them at run time; it only switches Allmon3 off, can back up its config, and can import favorites from an AllScan or Supermon install.

## Mode settings

Each mode's settings are on the Modes page, below the mode's controls. Passwords are write-only: `GET /api/modes` never returns them, only `secretsSet` (`{"password": true}`).

- **Ready.** A mode is ready when every Required field has a value of its own, a default or a station fallback. A stored password counts for a Required password field. BrandMeister and TGIF both require the hotspot password, so TGIF shows "Not set up" until one is saved.
- **Saving.** `PUT /api/modes/{mode}` replaces the mode's values; a password left empty or absent keeps the stored one, so other fields can be saved without retyping it.
- **Clear password.** A saved password has a small Clear button next to "Saved. Leave empty to keep it." After a confirm it sends `PUT /api/modes/{mode}` with `{"clear": ["password"]}`, which deletes only that stored secret. The other settings and anything typed but not yet saved stay. The mode then reports its missing password (TGIF becomes "Not set up"). An unknown key, a field that is not a password, or a key that is also given a new value is refused (`409 node_refused`) and nothing is saved.
- **Clear settings.** Always shown with the settings; disabled (with a tooltip) while nothing is saved for the mode or while a request runs. After a confirm it calls `DELETE /api/modes/{mode}`. If the mode is active the node drops it first (the radio goes idle), then removes the saved settings, passwords and the remembered target (`tune.json` entry) of that mode. The confirm text says so for an active mode.

- **Not set up.** A mode that is not configured shows a grey dot and a muted "Set up" in the Modes row and its settings header. Yellow and red are kept for real problems (connecting, not registered, an error).
- **Hiding modes.** An admin can hide modes nobody uses: "Hide this mode" in a mode's settings, or "Edit" on the Modes row, which shows every mode with a checkbox. Hidden modes leave the Modes row and the favorite and schedule mode pickers; the row ends with a "+N hidden" chip listing them, each with "Show". An active hidden mode is still shown, and a favorite or schedule that uses a hidden mode keeps it selectable when edited. Hiding never touches settings, and a fresh install shows every mode. The list is the app setting `hiddenModes` (`PUT /api/settings/hidden-modes`, docs/API.md).
- **EchoLink target search.** The EchoLink connect box searches the EchoLink directory (`GET /api/echolink/search`), not the AllStar list: each result shows the callsign, the node number, Online or Busy and the location and kind (link, repeater, conference, user). A pick fills in the callsign, which the mode resolves through the directory. The directory exists only while EchoLink is the active mode and logged in; before that the box says "EchoLink directory not loaded: log in to EchoLink first (make it the active mode)." and a typed callsign or node number still connects.
- **Now calling.** While someone talks on the network the Now calling box (Modes page) and the talker row of the Dashboard's mode card get a danger border, a light danger tint and a gently pulsing mic; while the local station sends, the same in the accent color. The pulse stops with `prefers-reduced-motion`. Idle looks as before. The talker's callsign, name and place are filled in by the server when the network sent only a DMR ID (docs/API.md "Talker and last-heard lookup").

## Host lists and master pickers

nodespot (the web app, not the node) downloads the Pi-Star host files once a day (about every 24 hours, plus or minus an hour, the first check 60 seconds after the start; a failed check, for example with no network or an unset clock at boot, is tried again after 10 minutes): `DMR_Hosts.txt`, `YSF_Hosts.txt`, `P25_Hosts.txt`, `NXDN_Hosts.txt` and `M17_Hosts.txt` from `https://www.pistar.uk/downloads/` (the setting `hostlists.base_url` overrides the base). They are kept in `<datadir>/hostlists` with conditional requests. The AllStar node list (`internal/nodelist`) is downloaded separately, from `[nodelist] url`.

- **Settings > Station > Host lists** shows one row per list (AllStar nodes, DMR, YSF, P25, NXDN, M17) with its entry count, the time it was last replaced and the last error. An admin's Refresh button downloads all of them now, host lists first, then the node list. The host lists allow a manual refresh once every 10 minutes (one that downloaded nothing does not count); a refused refresh (too soon, or one already running) shows the server's message and fetches nothing. A list that fails to download keeps its last good copy and shows its error in the row. `GET /api/hostlists/status` (operator) and `POST /api/hostlists/refresh` (admin) are in docs/API.md.
- **BrandMeister master.** The setting is a type-to-search field over the BrandMeister masters in the DMR list (entries named `BM_<id>_<Country>`, shown as "3104 – United States", sorted by id; `GET /api/hostlists?mode=bm`). Typing filters the list by name, host or country (every word must match), arrow keys and Enter pick, Escape closes it, and any other text (`host` or `host:port`) is kept as typed. A line under the field says "From the list: <name>" when the value is a list entry and "Not in the list: used as typed." otherwise. A pick saves the bare host name, exactly as the former dropdown did. The list's port is the Homebrew port and is never used: the native BrandMeister client (`internal/dv/network/bm`) dials the address it is given and uses port 54006 when the address has none, so only a typed `host:port` changes the port. With no list downloaded the field is plain text with a note.
- **DMR (other master).** Same picker over `GET /api/hostlists?mode=dmr-other` (the DMR list without BrandMeister and TGIF entries). A pick saves `host:port` from the list and, when the password field is empty and none is saved, fills in the list's password (a shared public value such as `passw0rd`). Typed text that is not a list entry is kept as `host` or `host:port`; the Homebrew client uses port 62031 when there is none.
- **TGIF.** There is no picker. With no saved address the Homebrew client connects to `tgif.network` port 62031 (`ctl.TGIFAddress`, `ctl.TGIFPort`). Following the DMR list's first TGIF entry is not done: the node is a separate process under another user (`nodespot-node`) with no access to the web app's data directory, so it cannot read the list, and handing it the address would need a new control-socket call or a shared directory. `hostlists.TGIFFromFile` and `hostlists.TGIFMaster` are kept for that. The TGIF password is never taken from a list.
- **Generic fields.** A text field with `hostList: "bm"` or `"dmr-other"` in `GET /api/modes` (`mode.Field.HostList`) is drawn as the searchable picker by the Modes page; the saved value stays plain text, so API clients that set the field directly are unaffected.
- **Empty lists.** When a mode's reflector or talkgroup list is empty (not downloaded yet), the Modes page says "The YSF reflector list hasn't downloaded yet. See Settings > Station > Host lists." under the search.

## Software AMBE vocoder

BrandMeister, TGIF, other DMR masters, YSF and NXDN carry AMBE voice, and the echo test uses it when its codec is set to AMBE (the other codecs need nothing extra; with the vocoder down the echo test still works and plays back as recorded, with an error shown). The node encodes and decodes it with the software AMBE vocoder package (P25 and M17 are computed in Go and need nothing extra). `install.sh` does not install the software AMBE vocoder by default; `NODESPOT_VOCODER=1` makes it do so at the end of the install, by running the same install synchronously (`nodespot-helper vocoder-install --wait`, which exits non-zero with a message on an unsupported platform or a failed job; the installer then only warns and still exits 0). An admin installs it from a DMR, YSF or NXDN mode's Settings section on the Modes page (the echo test card has no settings section; with the AMBE codec picked and no vocoder it shows a note linking to the DMR settings): when the picked mode needs AMBE and the vocoder is not installed or not running, the page shows "Needs the software AMBE vocoder" with an Install button. The helper then adds the package repository for it and its pinned signing key, installs the package (plus `qemu-user-static` on boards that are not armhf), keeps the service local-only with a systemd drop-in, and enables and starts it. It is safe to press again. The API is `GET /api/vocoder` and `POST /api/vocoder/install` (admin only).

## Sources

Code references point into the nodespot repository; size estimates are rough and not measured from a prototype.

- [AllStarLink/app\_rpt](https://github.com/AllStarLink/app_rpt) — `apps/app_rpt.c`, `apps/app_rpt/rpt_config.c`, `res/res_rpt_http_registrations.c`, `configs/rpt/extensions.conf`
- [asl3-update-nodelist](https://github.com/AllStarLink/asl3-update-nodelist)
- [ASL docs: DNS servers](https://allstarlink.github.io/adv-topics/dns-servers)
- [ASL docs: node resolution](https://allstarlink.github.io/adv-topics/noderesolution)
- [QSO One is much more stable now](https://community.allstarlink.org/t/qso-one-is-much-more-stable-now-still-looking-for-more-beta-testers-to-get-all-the-edge-cases/24651)
- [QSO One: looking for beta testers](https://community.allstarlink.org/t/looking-for-beta-testers/24526)
- [Can't create server to get started as a new user](https://community.allstarlink.org/t/cant-create-server-to-get-started-as-a-new-user/24926)
- [IP address check and reject on attempt to connect](https://community.allstarlink.org/t/ip-address-check-and-reject-on-attempt-to-connect/16985)
- [IP address does not match, stale DNS entry](https://community.allstarlink.org/t/ip-address-does-not-match-stale-dns-entry/22231)
- [Unable to connect to certain nodes](https://community.allstarlink.org/t/unable-to-connect-to-certain-nodes/17662)
- [Registration issues](https://community.allstarlink.org/t/registration-issues/10648)
- [Can't connect to HamVOIP node](https://community.allstarlink.org/t/cant-connect-to-hamvoip-node/24494)
- [ASL3 software update 2025-11-14](https://community.allstarlink.org/t/allstarlink-v3-software-update-release-2025-11-14/23960)
- [nodes.allstarlink.org DNS service update](https://community.allstarlink.org/t/nodes-allstarlink-org-dns-service-update/21879)

### Radio and Audio settings (Settings > Station > Radio and Audio)

Settings > Station has two tabs for the radio. **Radio** (`#/settings/station/radio`) holds the RF side: the interface, the SA818 card (channel, volume, tail tone, filters and Program), the timers and delays, the station ID, and a collapsed Advanced section with "Radio audio is flat (data port or discriminator)", the PL filter and the mic boost. **Audio** (`#/settings/station/audio`; the old `#/tune` and `#/settings/audio-levels` links redirect here) holds the levels: a two-step tuning wizard, the AGC each way, and a collapsed Advanced section with the static gain, the AGC's maximum gain and the sound chip's counters. Each tab saves only its own `advanced` keys over the station as saved now, so a change on one tab never undoes the other.

Tuning, step 1 (receive): "Key up a handheld and talk." A live bar of `GET /api/radio/input` shows that sound is arriving (the old live numbers are gone). After each transmission of at least 0.5 s of speech a "Last transmission" card gives one plain sentence and one action: "Sounds good. Nothing to change.", "Too quiet: move the slider right a little, then talk again." or "Distorting: move the slider left a little, then talk again." The numbers (speech and peak level, AGC gain, level out, clipped samples) sit in a collapsed "Details". The card comes from `radio.lastRx`: the speech level is the RMS of the gated speech frames, measured after the static receive gain and before the AGC, so the AGC cannot hide a quiet input; "too quiet" means the AGC was at its maximum gain for most of the speech, "distorting" means clipped samples or a peak at or above -1 dBFS. Kerchunks under 0.5 s do not replace the previous result. The slider is the sound chip's capture level in its own steps, labeled in dB when the chip reports a dB table (TLV, read with `SNDRV_CTL_IOCTL_TLV_READ`), else "step N of M"; chips without steps get a percent slider. Else the slider is Receive gain (`advanced.rxGainDb`, saved when released). **Auto-set** (admin, chips with receive steps) measures raw capture before any gain while you talk for a few seconds, then sets the lowest step whose speech level reaches the AGC target minus its maximum gain plus 6 dB, with the peak at or below -3 dBFS (no clipping wins if both cannot hold). Without a dB table it assumes 1 dB per step and the result is marked estimated. It fails with a plain message when there is no carrier within 15 s, too little speech, or the signal is too quiet even at the highest step ("check the radio's volume"). There is no Auto-set for transmit because the node cannot hear what it sends. Step 2 (transmit): "Send test tone" keys the transmitter with a 1 kHz tone for 5 seconds (`POST /api/radio/tone`), with Stop (`DELETE /api/radio/tone`), and a shortcut to the echo test. The slider is the playback mixer level in steps (dB when known), else Transmit gain (`advanced.txGainDb`). The Station group is admin only; the controls are read-only for anyone else.

"Tune your audio" reminder: the node remembers whether the receive audio was tuned in `station.audioTuned` = `{at, interface, dismissed?}` (`at` RFC 3339; `interface` is `<preset>:<USB id>` of the radio in use, from `HardwareID`). `Engine.Tune` with a receive level sets it, so Auto-set (which applies its step through `Tune`) and the slider (`PUT /api/radio`) both do; so does a changed `radio.rxVolume` in `SaveStation`. `Engine.DismissTuning` (control op `radio_tuning_dismiss`, client `RadioTuningDismiss`, `POST /api/radio/tuning/dismiss`) sets it with `dismissed: true` and no level. `SaveStation` always keeps the stored record. `migrate` (run at Start) records a legacy config that has no record but a stored audio level (`rxVolume`, `txVolume`, `rxBoost`, `advanced.rxGainDb`/`txGainDb`, `rxAgcTargetDb`/`txAgcTargetDb`, `agcMaxDb`) as tuned for the hardware in use, so it never reminds an existing station; a fresh config has no record. `Status.Radio.NeedsTuning` (`radio.needsTuning`) is true when a radio is present, the chip has capture steps and `Station.Tuned(HardwareID)` is false; a different recorded interface or chip counts as untuned, a dismissal included. The dashboard shows the banner to admins only, with "Tune now" (`#/settings/station/audio?tune=1`: step 1 opens and Auto-set is scrolled to and highlighted for a few seconds) and "Dismiss"; Auto-set and the slider refresh the node status, so the banner goes at once.

Test tone: the node keys PTT, waits for the key-up delay, then plays a 1 kHz sine for the asked time (1 to 10 s, default 5) at the level network speech reaches the radio: the network to radio AGC target when that AGC is on, else -22 dBFS plus the transmit gain (RMS; the peak is 3 dB higher, at most -1 dBFS). It is refused (`409 node_refused` with the reason) when there is no radio, the node is already transmitting, the receiver has a carrier, or the active mode is talking or has audio queued. It stops early when a local station or the network starts talking, and counts as a transmission for the station ID. The node status has `radio.testTone: true` while it plays; the node publishes a `tone` event when it starts and ends.

The tabs edit `station.advanced`, `station.id` and the `station.radio` module values (`sa818Filter`, `sa818Volume`, `sa818Tail`). A save is validated by `Config.Validate`, written, and applied to the running engine at once (`paramsFor` is rebuilt on every commit); no restart is needed. Only radio wiring changes restart the node. Empty or 0 means the default; the form shows it as a placeholder and does not accept 0. The key-up delay is the exception: empty is its default and 0 turns it off.

| Setting | Key | Range | Default |
|---|---|---|---|
| Receive gain, radio to network | `advanced.rxGainDb` | -20 to 20 dB | 0 |
| Transmit gain, network to radio | `advanced.txGainDb` | -20 to 20 dB | 0 |
| Radio audio is flat: software pre-emphasis (transmit) and de-emphasis (receive) | `advanced.flatAudio` | on/off | off |
| PL filter (receive) | `advanced.plFilter` | on/off; unset is automatic | on, off for an SA818 |
| SA818 filters | `radio.sa818Filter.{emphasis,highPass,lowPass}` | on/off each | emphasis off, high-pass and low-pass on |
| SA818 volume | `radio.sa818Volume` | 1 to 8 | 8 |
| SA818 tail tone | `radio.sa818Tail` | open, closed or leave as is | leave as is (nothing sent) |
| Receive boost | `radio.rxBoost` | on/off, only if the chip has the control | chip default |
| Receive-on delay | `advanced.rxOnDelayMs` | 0 to 1000 ms | 0 |
| Key-up delay | `advanced.txOnDelayMs` | 0 to 1000 ms; 0 is off | 500 |
| Transmit-off delay | `advanced.txOffDelayMs` | 0 to 2000 ms | 0 |
| AGC, network to radio | `advanced.modeAgc` | on/off | on |
| AGC, radio to network | `advanced.radioAgc` | on/off | on |
| AGC target, radio to network | `advanced.rxAgcTargetDb` | -40 to -6 dBFS | -22 |
| AGC target, network to radio | `advanced.txAgcTargetDb` | -40 to -6 dBFS | -22 |
| AGC maximum gain (both directions) | `advanced.agcMaxDb` | 1 to 30 dB (0 = default) | 18 |
| Hang time | `advanced.hangMs` | 1 to 10000 ms | 500 |
| Transmit timeout, both ways | `advanced.txTimeoutS` | 1 to 1800 s | 180 |
| Station ID | `id.enabled` | on/off | on |
| ID interval | `id.intervalS` | 1 to 600 s | 600 |
| Tail window | `id.tailWindowS` | 1 to 600 s | 60 |
| Final ID after quiet | `id.endQuietS` | 1 to 600 s | 60 |
| CW speed | `id.wpm` | 5 to 60 | 25 |
| CW pitch | `id.pitchHz` | 200 to 2000 Hz | 800 |
| CW level | `id.levelDb` | -60 to -1 dBFS | -24 |
| Voice file | `id.voiceFile` | absolute path of an 8 kHz mono WAV, else CW | CW |

Retired keys are migrated when the config is read: `agcTargetDb` becomes both `rxAgcTargetDb` and `txAgcTargetDb` (unless they are set), and `preEmphasis` or `deEmphasis` set to true becomes `flatAudio: true` (unless it is set); the old keys are dropped on the next save.

Audio processing order. Receive (radio to network): PL filter, software de-emphasis (flat audio), receive gain, AGC. Transmit (network to radio): transmit gain, software pre-emphasis (flat audio), AGC. The core runs at 8 kHz in 20 ms frames; the filters are designed for that rate (`internal/native/audio/dsp.go`). The static gain is exact (a power of ten of the dB value) and soft-limited. The emphasis filters are first order with a 75 microsecond constant (corner 2122 Hz), normalized to 0 dB at 1 kHz, and pre-emphasis is the exact inverse of de-emphasis. The PL filter is a 4th-order Butterworth at 300 Hz (about -3 dB at 300 Hz and more than 20 dB down at 100 Hz).

SA818 filters: the module's command is `AT+SETFILTER=<emphasis>,<highpass>,<lowpass>`, where 1 bypasses that filter and 0 turns it on (the programming manual's `AT+SETFILTER=0,0,0` turns all three on). The `radio.sa818Filter` flags mean "on" in the config, the API and Settings, and the node writes the inverse to the wire. An unset flag takes the default: emphasis off, high-pass on, low-pass on (`AT+SETFILTER=1,0,0`). The high-pass removes the PL tone from the received audio. The flags are written to the module with the rest of its values: by the Program button, when a save changes the channel, volume, tail tone or filters, and at boot when any of these differs from what the state file records. Volume is `AT+DMOSETVOLUME=<1 to 8>`. The tail tone is `AT+SETTAIL=1` (open) or `AT+SETTAIL=0` (closed), sent after the filters and only when `radio.sa818Tail` is set; unset, nodespot never touches the module's tail. Settings shows the squelch, volume, tail tone and filters recorded in `/etc/nodespot/sa818.json` when the module was last programmed (filters as "on" flags), or "unknown until programmed". The state file is version 3, which adds the tail tone. A version 2 file still reads (its tail is unset, as nodespot never sent one then), so an upgrade does not program the module again. A version 1 file was written by builds that sent the flags inverted, so it reads as unknown and the module is programmed again at boot. On an SA818 radio the PL filter is off unless set.

Delays: with a receive-on delay, a carrier must be present for `rxOnDelayMs` before the node starts passing receive audio, so that much of the start of a transmission is dropped. The key-up delay holds network audio for `txOnDelayMs` after the node keys PTT, so the transmitter (an SA818 takes a moment) is up before the first word; the audio waits in the queue, none of it is dropped. It applies only when PTT goes up: a transmission that starts during the hang plays at once. The transmit-off delay is added to the hang time before PTT is released.

Receive boost: `PUT /api/radio` accepts `rxBoost`; the control is the sound chip's microphone boost ALSA mixer control, and `GET /api/radio` reports `BoostAvailable` and `Boost`. A chip without the control ignores the setting.

The web tabs save by reading the current station, replacing only their own keys, and PUTting it to `/api/station`, so other fields (and unknown keys such as `md380`) survive.
