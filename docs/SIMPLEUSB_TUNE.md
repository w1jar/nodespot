# SimpleUSB audio tuning from the web UI

> Removed: this describes a feature of the archived aslcp 2.x that needed Asterisk. nodespot runs its own node; see [NATIVE_NODE.md](NATIVE_NODE.md). Kept for reference.

Status: implemented in 1.1.6 (tone removed in beta.8) (admin-only "Audio levels" page (named "Audio Tuning" before 2.4.0), `/api/radio`). Sections 1 to 9 are the original design; "Implementation notes" near the end lists what differs and what is not yet verified on hardware. Node numbers below are placeholders (1999, 1998).

> **The TX test tone was removed in 1.1.8.** `susb tune flash` is meant to key three 1 s bursts. On the bench the tones were intermittent, even when `susb tune flash` was run directly with `asterisk -rx`, with no nodespot involved, while `simpleusb-tune-menu` played them consistently. The likely cause is a race with app_rpt on the channel generator, which is in the driver and not something nodespot can fix. The maintainer decided the panel does not need the feature. The page now says: for a steady test tone (e.g. with a deviation meter), use `simpleusb-tune-menu` in a terminal. Everything below that describes the tone, `flash`, the channel-up pre-flight check, tone rate limits, `POST /api/radio/{dev}/tone` and the dedicated AMI connection is the original design, kept as history; none of it is in the code any more.

## 1. Summary and verdict

AllStarLink 3 ships `simpleusb-tune-menu`, a terminal program for setting RX/TX audio levels and a few related options of `chan_simpleusb` devices. It has no audio or USB code of its own: it shells out to `asterisk -rx "susb ..."` and prints what comes back. Everything it does is a `susb` CLI command, and the CLI commands are plain Asterisk CLI entries, so they can be run through the AMI `Command` action that nodespot already uses.

**Verdict: feasible, with three things the browser version must do differently from the terminal tool.**

1. **The live displays cannot be reused.** The tune menu's RX meter (`menu-support b`), the TX test tone with keying (`menu-support l`, `fK`, `gK`) and the COS/CTCSS/PTT view (`v`) all loop on `poll(fd)` waiting for a keypress. Over AMI the CLI "fd" is a temporary file, which is always readable, so these loops exit immediately (to be confirmed on hardware, section 8). Replacements exist: the one-shot RX audio statistics (`menu-support Y`) for the meter, and the blocking `susb tune flash` for a bounded test tone.
2. **The active device is global state.** Every `susb tune ...` command acts on the "active" device set by `susb active <name>`. With two nodes on one Pi, nodespot must serialize tune operations, set the active device before each operation, verify it, and restore it afterwards.
3. **Saving is done by Asterisk, not nodespot.** `susb tune save` rewrites `/etc/asterisk/simpleusb.conf` itself (and the interface EEPROM, if enabled). That bypasses the nodespot config history and conflict detection, so nodespot needs a small follow-up step to record the change.

No root privilege is needed for the tuning commands themselves. The main service owns them (AMI); the root helper is needed only for the optional history snapshot after a save (section 5.3).

## 2. Findings

Sources (AllStarLink `app_rpt`, GitHub):

- Tune menu: [`utils/simpleusb-tune-menu.c`](https://github.com/AllStarLink/app_rpt/blob/master/utils/simpleusb-tune-menu.c)
- Channel driver and CLI handlers: [`channels/chan_simpleusb.c`](https://github.com/AllStarLink/app_rpt/blob/master/channels/chan_simpleusb.c) (functions `susb_tune`, `susb_active`, `tune_menusupport`, `tune_write`, `tune_flash`, `_send_tx_test_tone`, `tune_rxdisplay`)
- Audio statistics and polling helpers: [`res/res_usbradio.c`](https://github.com/AllStarLink/app_rpt/blob/master/res/res_usbradio.c) (`ast_radio_print_audio_stats`, `ast_radio_poll_input`, `ast_radio_wait_or_poll`) and [`include/asterisk/res_usbradio.h`](https://github.com/AllStarLink/app_rpt/blob/master/include/asterisk/res_usbradio.h)
- AMI `Command` implementation: [`main/manager.c` `action_command`](https://github.com/asterisk/asterisk/blob/master/main/manager.c)
- Release tags checked: `3.0.0`, `3.5.0`, `3.10.0` (e.g. `https://github.com/AllStarLink/app_rpt/blob/3.10.0/channels/chan_simpleusb.c`).

The ASL3 manual (`allstarlink.github.io`, "simpleusb" and "simpleusb-tune-menu" pages) could not be fetched from the research environment (host not reachable). Operator-procedure details (target levels, how to read the meter against deviation) are therefore **not** verified against the manual; see open questions.

### 2.1 How the tune menu works

- `doastcmd()` forks `/usr/sbin/asterisk -rx "<cmd>"` and reads the output through a non-blocking pipe. `astgetresp()` echoes the output to the terminal and watches stdin; any typed line ends a live display. That is how the "live" screens stop.
- On start it optionally runs `susb active <device>` (command-line `-n`) and checks that the reply begins `Active (command) Simple USB Radio device set to `.
- It reads the whole parameter block with `susb tune menu-support 0+4` and parses 16 comma-separated integers.
- Every change is a one-line `susb tune menu-support <letter><value>` command. The menu holds no state of its own besides a local "keying" flag.

### 2.2 `0+4` parameter block (one line, 16 integers)

Order, from `tune_menusupport` case `'0'` (the order is frozen "for backward compatibility"; new fields are only appended):

| # | Field | Meaning / values |
|---|---|---|
| 1 | txmixaset | Tx A level, 0-999 |
| 2 | txmixbset | Tx B level, 0-999 |
| 3 | echomode | 0/1 |
| 4 | rxboost | 0/1 |
| 5 | preemphasis | 0/1 |
| 6 | deemphasis | 0/1 |
| 7 | plfilter | 0/1 |
| 8 | invertptt | 0/1 (shown by the menu as PTT mode "Ground"/"Open") |
| 9 | rxcdtype ("carrierfrom") | internal code: 0 no, 3 usb, 4 usbinvert, 5 pp, 6 ppinvert (1 and 2 are "N/A") |
| 10 | rxsdtype ("ctcssfrom") | internal code: 0 no, 1 usb, 2 usbinvert, 4 pp, 5 ppinvert (3 is "N/A") |
| 11 | rxondelay | in 20 ms frames |
| 12 | txoffdelay | in 20 ms frames |
| 13 | rxmixerset | Rx voice level, 0-999 |
| 14 | micplaymax | hardware mixer maximum (informational) |
| 15 | spkrmax | hardware mixer maximum (informational) |
| 16 | micmax | hardware mixer maximum (informational) |

The plain `0` form returns only the first 12 values. `0+4` is not documented in the header comment of the menu source; the tune menu parses exactly 16 and exits if the count differs, so nodespot should accept 16 and treat anything else as "unsupported version". It is present in `3.5.0` and `3.10.0` and absent in `3.0.0`.

### 2.3 Command set

All commands are prefixed `susb tune menu-support ` (menu-support) unless shown otherwise. Source of truth: `tune_menusupport()` in `chan_simpleusb.c`.

| Letter | Effect |
|---|---|
| `0+4` / `0` | parameter block (above) |
| `1` | comma-separated list of configured device (stanza) names, e.g. `1999,1998` |
| `2` | print parameters: `Active radio interface is [name]`, `Device String is ...`, `Device Serial is ...` (if any), `Card is N`, `Rx Level currently set to N`, `Tx A Level ...`, `Tx B Level ...` |
| `3` | device list without the active one |
| `c` / `c<N>` | query / set Rx voice level (`rxmixerset`). Digits only, 0-999. Replies `Channel <name>: Changed setting on RX Channel to N` or `Channel <name>: Entry Error, ...` |
| `f` / `f<N>` | query / set Tx A level (`txmixaset`), 0-999. `fK<N>` and `fK` additionally key the transmitter with a 1000 Hz tone (see 2.5) |
| `g` / `g<N>` | same for Tx B (`txmixbset`) |
| `k<0/1>` | echo mode |
| `m<0/1>` | rxboost (20 dB attenuator removed when 1) |
| `n<0/1>` | pre-emphasis |
| `o<0/1>` | de-emphasis |
| `p<0/1>` | PL filter |
| `q<0/1>` | PTT mode (`invertptt`) |
| `r<code>` | carrierfrom, internal code (table above). **Unchecked index into a string array in the driver**, see 2.7 |
| `s<code>` | ctcssfrom, internal code. **Unchecked index**, see 2.7 |
| `t<N>` / `u<N>` | rxondelay / txoffdelay in 20 ms frames. The menu accepts 0-999; the driver clamps at 3000 frames (60 s) |
| `l` | test tone ("flash"), keys PTT, see 2.5 |
| `v` | COS/CTCSS/PTT live status display (loops until keypress) |
| `y` / `Y` | RX audio statistics: `y` repeats every second until keypress, **`Y` prints once** |
| `z` / `Z` | TX audio statistics, same pattern; prints `Tx not keyed` when not transmitting |
| `b` | RX tune display, a live ASCII bar graph (loops until keypress) |
| `j` | save (see 2.6) |

Set commands for the boolean options reply e.g. `RxBoost changed to Enabled` (any digit above `0` means enabled). A set without a value returns `... is currently Enabled|Disabled`.

Non-menu forms (`susb tune <fn>`): `rx [N]`, `txa [N]`, `txb [N]` (set range 0-999, reply `Changed setting on RX Channel to N` / `Current setting on Rx Channel is N`), `rxd` (live display), **`flash`**, `save`, `load` (load from EEPROM), `swap <other>`, plus capture helpers (`rxcap`, `txcap`, `nocap`) that should not be exposed.

Other related commands: `susb active` (reply `Active Simple USB Radio device is [name].`), `susb active <name>` (reply `Active (command) Simple USB Radio device set to [name]` or `No device [name] exists`), `susb active show` (one line per device: `Device [name] is assigned to device=<usb path> card=N`), `susb show settings` (same as `menu-support 2`).

### 2.4 RX level meter

The terminal meter (`tune_rxdisplay`, `menu-support b`) enables `measure_enabled`, then every 100 ms draws a bar from `apeak` (peak of the discriminator audio), scaled `apeak * 75 / 16384`, with a header marking "3KHz" and "5KHz" columns. It only draws while the receiver is keyed. It cannot be used over AMI.

What can be used: `menu-support Y` prints one line from a rolling one-second statistics buffer (50 frames of 20 ms) that the driver fills continuously from the capture stream, whether or not the meter is enabled:

```
RxAudioStats: Pk  -2.1  Avg Pwr -32  Min -60  Max -12  dBFS  ClipCnt 0
```

(format string in `ast_radio_print_audio_stats`: `"%sAudioStats: Pk %5.1f  Avg Pwr %3.0f  Min %3.0f  Max %3.0f  dBFS  ClipCnt %u"`). Pk is the largest sample in the last second in dBFS, Avg/Min/Max Pwr are per-frame RMS power in dB, ClipCnt counts clipped samples. This is measured on the capture buffer after the hardware input mixer (so it tracks `rxmixerset`) and before the driver's DSP. It is a **relative** level, not deviation: calibrating it to deviation needs a known reference signal (section 8). `Y` is present in `3.5.0` and `3.10.0`, absent in `3.0.0`.

COS, CTCSS and TX keyed state need no CLI: nodespot already receives `RPT_RXKEYED` / `RPT_TXKEYED` and the node status, which cover the same information as the `v` display (hardware COS/CTCSS inputs separately would need `v`, which is not reachable; not required for the first version).

### 2.5 TX test tone and keying

- `susb tune flash` (non-menu form, `intflag = 0`): three bursts of a 1004 Hz test tone (the tone generator frequency in `_send_tx_test_tone`; the prompt text says 1000 Hz), 1 s on and 1 s off, which keys the transmitter via `txtestkey`. It waits with `usleep`, so it works without a TTY and **blocks the CLI command for about 5 s**. The tone is generated into the node's channel (`o->owner`), i.e. it goes out the local radio, not to linked nodes.
- `menu-support l`, `fK[N]`, `gK[N]` use `intflag = 1`: they poll the CLI fd every 50 ms to see whether the user pressed a key. Over AMI the fd is a regular temporary file (`mkstemp` in `action_command`), which `poll()` always reports readable, so the tone starts and is cancelled on the first poll. Net effect over AMI: a few milliseconds of keying at most, no usable tone. **Do not use these forms from nodespot.**
- Consequence for the UX: levels are set with a non-keying command (`menu-support f<N>` without `K`, or `susb tune txa <N>`), then a separate "send test tone" action runs `susb tune flash`. The tone length is fixed (about 5 s per call), which is also the safety bound: there is no way to hold the transmitter keyed indefinitely through this path. A continuous tone for deviation measurement is then "repeat flash" (open question 3).
- TX statistics: `Z` prints `TxAudioStats: ...` only while keyed.

### 2.6 Saving

`menu-support j` and `susb tune save` both call `tune_write(o)` and reply `Saved radio tuning settings to simpleusb.conf`. `tune_write`:

- loads `simpleusb.conf` with comments preserved (`CONFIG_FLAG_WITHCOMMENTS | CONFIG_FLAG_NOCACHE`), finds the stanza named after the active device, updates these keys and writes the file back with `ast_config_text_file_save2`:
  `rxmixerset`, `txmixaset`, `txmixbset`, `rxboost`, `preemphasis`, `deemphasis`, `plfilter`, `invertptt`, `carrierfrom`, `ctcssfrom`, `rxondelay`, `txoffdelay` (booleans written as `yes`/`no`, signals as `no|usb|usbinvert|pp|ppinvert`);
- may also write `devstr` / `serial` (keeps blank automatic assignment when there is one automatic device; writes an explicit selector when several devices were auto-assigned);
- values inherited from a template (`[node](!)` blocks, as in the ASL3 default `simpleusb.conf`) are written into the node stanza rather than into the template;
- when `wanteeprom` is set (interface has an EEPROM and `eeprom = 1`), also writes `rxmixerset`, `txmixaset`, `txmixbset` to the interface EEPROM. A direct file edit by nodespot would **not** update the EEPROM; `susb tune save` does.

There is no separate `simpleusb_tune_*.conf` in ASL3 `chan_simpleusb`: tuning goes to `simpleusb.conf`. (The older `usbradio_tune_*.conf` style belongs to legacy `chan_usbradio` / app_rpt versions; `chan_usbradio` in the current tree also writes to its main config file via the same pattern, see section 6.) Echo mode (`echomode`) is not saved.

`susb tune load` re-reads the EEPROM into the running device (not needed for v1).

Un-saved changes live only in the running channel. A `module reload chan_simpleusb.so` or an Asterisk restart returns to the file values. nodespot's config editor already runs exactly that reload after editing `simpleusb.conf`, so it would silently discard un-saved tuning (section 7).

### 2.7 Pitfalls found in the driver

- **`r<code>` / `s<code>` have no bounds check.** The value is `atoi()`ed and used to index `cd_signal_type[7]` / `sd_signal_type[6]` for the reply message, and stored unchecked as the detection mode. An out-of-range value can read out of bounds in the Asterisk process, and the "N/A" slots (carrier 1 and 2, ctcss 3) are not valid modes. nodespot must only ever send whitelisted codes.
- `t<N>` / `u<N>` use `atoi` with no sign check; negative numbers are not clamped. Validate 0-3000 (frames) server-side; offer 0-999 in the UI at most (what the terminal menu allows) or show milliseconds (`frames * 20`).
- `q` (PTT mode) when queried prints the state of `plfilter` (cosmetic driver bug). Read PTT mode from `0+4` field 8, never from the `q` query reply.
- `rx`/`txa`/`txb` and `Y` replies carry no device name, so they cannot prove which device they ran on; the `menu-support c|f|g` replies do (`Channel <name>:`), and `2` shows `Active radio interface is [<name>]`.
- `flash` and the tone path call into `o->owner`; if the device exists but the node channel is not up (app_rpt not started, channel torn down) the behavior is untested and could fault. nodespot must check node/channel health first (section 6.4).
- A device whose USB interface is unplugged replies `Device <name> is selected, the associated USB device string <devstr> was not found`; all level/tone/meter commands print this instead of results.

### 2.8 Reachability over AMI

All set/query commands in 2.3 (everything except the three loops `b`, `v`, `y`/`z`, and the keyed forms `l`, `fK`, `gK`) are reachable through `Action: Command` and need only that the AMI user has `command` write permission (nodespot already uses `Command` for `rpt cmd`, config reloads and `core restart`). Output comes back as `Output:` header lines (modern Asterisk) or text until `--END COMMAND--` (legacy); `ami.Conn.Command` already normalises both. Notes:

- One-line replies map to one `Output:` line. Trailing blank lines are trimmed by `Command`.
- Output may contain a carriage return `\r` (live displays) that must be stripped before parsing.
- Each `Command` creates a temp file `/tmp/ast-ami-XXXXXX` (removed afterwards). On a Pi with the SD card, keep polling modest (section 4.2).

## 3. Command mapping (UI control to CLI to parse)

`<dev>` is the device stanza name (equals the node number in the default ASL3 layout, e.g. 1999). Every operation is preceded by `susb active <dev>` (section 5.1).

| UI control | Command (via AMI `Command`) | Parse / success check |
|---|---|---|
| Device list | `susb tune menu-support 1` | split on `,` (empty line = no devices) |
| Active device (read, restore) | `susb active` | `Active Simple USB Radio device is [<dev>].` |
| Select device | `susb active <dev>` | must start with `Active (command) Simple USB Radio device set to [<dev>]`; `No device [...] exists` is an error |
| Device identity | `susb active show`, `susb tune menu-support 2` | `Device [<dev>] is assigned to device=<path> card=N`; `Device String is`, `Device Serial is`, `Card is`; show "not found" if the `selected, the associated USB device string ... was not found` text appears |
| Load all values | `susb tune menu-support 0+4` | 16 integers, regex `^\d+(,\d+){15}$`, else "unsupported" |
| Rx voice slider (0-999) | `susb tune menu-support c<N>` | `Channel <dev>: Changed setting on RX Channel to <N>`; `Entry Error` is an error; check `<dev>` matches |
| Tx A slider | `susb tune menu-support f<N>` (no `K`) | `Channel <dev>: Changed setting on TX Channel A to <N>` |
| Tx B slider | `susb tune menu-support g<N>` (no `K`) | `Channel <dev>: Changed setting on TX Channel B to <N>` |
| Live RX meter | `susb tune menu-support Y` | `RxAudioStats: Pk <f>  Avg Pwr <n>  Min <n>  Max <n>  dBFS  ClipCnt <u>` (regex on the numbers; `-96` means silence) |
| TX audio check (during tone) | `susb tune menu-support Z` | `TxAudioStats: ...` or `Tx not keyed` |
| TX test tone | `susb tune flash` (blocks about 5 s) | `Channel <dev>: USB Device Flash starting.` ... `Flash completed.`; error text instead means refused |
| Rx boost toggle | `susb tune menu-support m<0\|1>` | `RxBoost changed to Enabled\|Disabled` |
| Pre-emphasis | `... n<0\|1>` | `Pre-emphasis changed to ...` |
| De-emphasis | `... o<0\|1>` | `De-emphasis changed to ...` |
| PL filter | `... p<0\|1>` | `PL Filter changed to ...` |
| PTT mode (invert) | `... q<0\|1>` | `PTT mode changed to Open\|Ground` |
| Echo mode (optional, not saved) | `... k<0\|1>` | `Echo Mode changed to ...` |
| Carrier from (no/usb/usbinvert/pp/ppinvert) | `... r<code>`, code from {0, 3, 4, 5, 6} | `Carrier From changed to <name>`; name must not be `N/A` |
| CTCSS from | `... s<code>`, code from {0, 1, 2, 4, 5} | `CTCSS From changed to <name>` |
| RX on delay | `... t<frames>` | `RX On Delay From changed to <n>` |
| TX off delay | `... u<frames>` | `TX Off Delay From changed to <n>` |
| Save | `susb tune menu-support j` | `Saved radio tuning settings to simpleusb.conf` (no failure text is printed by the driver on write errors; they only go to the Asterisk log, so nodespot verifies by re-reading the file, see 5.3) |
| Revert (discard unsaved) | none in the driver. Re-apply the values last read/saved with the `c/f/g/m/...` commands (nodespot remembers them), or `module reload chan_simpleusb.so` | re-read `0+4` and compare |

## 4. Proposed UX

A new page "Radio" (hash route `#/radio`, lazy-loaded like Config.svelte) in the main menu, visible when `simpleusb.conf` has at least one device and `chan_simpleusb` answers. Admin only to change anything; operators get a read-only view and the meter (see 5.4).

### 4.1 Layout

1. **Device selector** (only shown with 2 or more devices): tabs or a dropdown listing each stanza, labelled with the node number from `rpt.conf` (`rxchannel = SimpleUSB/<dev>`) and its USB identity and "not found" state. Switching device does not touch the radio; it only changes which device the page reads and writes. The page always shows which device is being tuned, in a fixed header line.
2. **Receive card**: live RX meter (horizontal bar plus numeric Pk in dBFS and a clip indicator), a state chip fed by the existing `RPT_RXKEYED` event, the Rx voice slider (0-999, with a number field), "Carrier from" and "CTCSS from" selects, Rx boost, De-emphasis, PL filter switches, RX on delay.
3. **Transmit card**: Tx A and Tx B sliders (only Tx A shown unless Tx B is relevant, i.e. non-zero or the user expands "Tx B"), Pre-emphasis, PTT inversion, TX off delay, and the **Send test tone** button with its confirmation (4.3).
4. **Footer bar**: "Unsaved changes" state, **Save to simpleusb.conf** and **Revert**.

Each slider writes on release (debounced, 300 ms), shows the confirmed value from the command reply, and reverts the control and shows the error if the reply is an error. A small "last device reply" line gives the exact text for support requests.

### 4.2 Live meter: polling over SSE

Server-side, one poller per open tuning session (not per browser tab): while at least one admin/operator has the meter open for device `<dev>`, nodespot issues `Y` at 2 Hz (the underlying window is 1 s, so more is useless) and pushes `{dev, pk, avgPwr, min, max, clip, ts}` to subscribers. Transport options:

- **SSE (recommended):** a dedicated `GET /api/radio/{dev}/meter` event stream, same pattern as the existing `/api/events` hub (pre-framed messages, slow client dropped, browser reconnects). One poller feeds all subscribers. Auto-stops 10 s after the last subscriber leaves. No polling from the browser.
- Browser polling of a REST endpoint (1-2 s) would work and is simpler, but multiplies AMI traffic and temp-file creation by the number of open tabs. Not recommended.

Guard rails for the poller: back off when the AMI client is disconnected; stop when the tab reports `visibilitychange` hidden (the page sends an explicit "pause" or just closes the stream); never run without an open stream; hard cap of 5 minutes per stream before it must be renewed with a click ("Meter paused, resume"), so a forgotten tab does not keep writing temp files on the SD card.

Display notes: show dBFS (peak hold for 1 s is what the driver gives), mark clipping (`ClipCnt > 0`) red. An optional reference marker is drawn at a configurable dBFS value once the bench calibration in section 8 is known (not hard-coded in v1).

### 4.3 TX test tone and keying confirmation

- The button reads "Send test tone (keys your transmitter for about 5 seconds)".
- Clicking opens a modal that says, in plain words: the transmitter of node 1999 will transmit a 1 kHz tone, three 1-second bursts over about 5 seconds, on the configured frequency; use a dummy load or ensure the channel is clear; station identification rules still apply; you are responsible for the transmission. Two controls: a checkbox "My transmitter is connected to a dummy load or I am authorised to transmit on this frequency" and a **hold-to-confirm** or typed confirmation (type the node number). The modal shows the current state of that node (idle / receiving / transmitting).
- The server enforces the safety bound, not the browser: tone only through `susb tune flash` (fixed length), at most one tone run at a time system-wide, minimum 10 s between runs per device, hard cap of 6 runs per 10 minutes per user, and refusal when the node is currently keyed (RX or TX) or has an active tune-tone run. While a run is in progress the UI shows a large "TRANSMITTING" banner with a progress bar and a **Stop** button. Stop closes the stream and marks the run cancelled in the UI only; the driver cannot be interrupted over AMI, so the banner stays until the 5 s run completes (the UI must not imply an earlier stop; label it honestly: "The tone cannot be cut short; ends in N s").
- Every run is written to the audit log (user, device, level A/B at the time, time, result).
- During the tone, poll `Z` once per second so the user sees the TX power/clip statistics while the test equipment measures deviation.

### 4.4 Save and revert

- "Unsaved changes" is computed by comparing the last `0+4` snapshot taken from `susb` with the values read from `simpleusb.conf` (via the config API) for that stanza. This also covers changes made behind nodespot's back with the terminal tool.
- **Save** runs `menu-support j` for the selected device, then re-reads `simpleusb.conf` and shows a diff of the keys that changed. If the value in the file does not match the running value after a save, the page reports "save may have failed, see Asterisk log".
- **Revert** re-applies the values from the file (not "reload the module": a reload would also drop changes that are unrelated and would re-initialize both devices; see 7).
- Navigation guard: leaving the page with unsaved changes asks for confirmation.
- A "Restore previous values" button is shown for the current session (nodespot keeps the values read when the page opened, in memory only).

### 4.5 Guided mode (later)

A short wizard on top of the same API: step 1 select device, step 2 RX (with a prompt to apply a known 1 kHz reference tone at 3 kHz deviation and a button "Hold peak"), step 3 TX (tone plus target deviation entered by the user), step 4 save. Not in the beta scope.

## 5. Backend design

### 5.1 Where it runs and what talks to what

```
Browser --HTTPS--> nodespot server (internal/radio, new) --AMI Command--> Asterisk (chan_simpleusb) --writes--> simpleusb.conf, interface EEPROM
                                        \--(optional, root helper op)--> config history snapshot
```

- New package `internal/radio` (or `internal/susb`) in the **main service**. It wraps `ami` the way `internal/rpt` does: a small interface (`Command(ctx, string) ([]string, error)`) so it can be tested against the existing `amitest` fake and the `--demo` simulated node.
- A **dedicated AMI connection** (a second `ami.Dial`, events off) is used for tuning rather than the shared `ami.Client`. Reason: the AMI connection runs one action at a time (a semaphore, `Conn.acquire`), so a 5 s `susb tune flash` on the shared connection would stall status polling and keepalive for the whole panel. A dedicated connection isolates this. If a second connection is considered too heavy, the fallback is to run `flash` on the shared client and accept a visible 5 s stall (not recommended).
- A package-level **tune mutex** serialises all tune operations across users and devices. Each operation is a transaction: acquire mutex, `susb active` (remember previous), `susb active <dev>`, run the command(s), verify the reply names `<dev>` where the reply carries a name (`c`, `f`, `g`, `2`, flash), restore the previous active device, release. Replies without a device name (`Y`, `rx`, `txa`) are only used immediately after an explicit `susb active <dev>` inside the same locked section.
- Operator-side tools running at the same time (SSH `simpleusb-tune-menu`, another AMI client) can still change the active device between nodespot's commands. The name check mitigates this; it cannot remove the race entirely (open question 6).

### 5.2 Input validation (all server-side)

Strict, fixed command strings only; no pass-through of user text:

- `<dev>`: must be in the list returned by `menu-support 1` (refreshed on each request, cached for a few seconds) and match `^[A-Za-z0-9_-]{1,32}$`.
- levels: integers 0-999; booleans: `0` or `1`; carrier code in {0,3,4,5,6}, ctcss code in {0,1,2,4,5}; delays: integers 0-3000 (frames).
- A `Device <name> is selected, the associated USB device string <devstr> was not found` reply is returned as `502 device_not_found` with a plain message (the interface is unplugged, or Asterisk cannot use it yet; on a fresh ASL3 install the installer applies ASL3's `usb`, `sound` and `hidraw` udev rules and reloads the USB sound driver when its `lowlatency` setting differs, and asks for a reboot only if that fails) instead of `radio_unexpected_output`.
- Output parse failures are returned as `502 radio_unexpected_output` with the raw first line (trimmed, no secrets) for diagnostics.

### 5.3 Save and the config history (helper involvement)

`susb tune save` is executed by Asterisk as the `asterisk` user, which already owns `/etc/asterisk`. Nothing here needs root. But nodespot's config engine (docs/CONFIG.md) keeps a git history in a root-only directory and detects outside edits by sha256, so:

1. Before saving, the server reads `simpleusb.conf` through the existing config read path and remembers its sha256.
2. After `menu-support j`, it reads the file again, parses it with `astconf`, and shows the diff to the user.
3. A new minimal helper operation (suggested name `config-record`, request `{path, message, author}`) commits the file's current on-disk content to the history repository as an "external change" with a message like `Radio tuning saved from web UI (device 1999)`. The helper validates the path like the other `config-*` ops (only `simpleusb.conf`/`usbradio.conf` accepted for this op), never trusts content from the server, and reads the file itself. This keeps rollback via the existing "restore" flow working and keeps `config-apply` base hashes consistent. If the helper op is not available (older helper), skip it: the next config-apply from the editor will show a conflict, which is the correct behavior.

Alternative considered and rejected for v1: **writing the values via the config editor pipeline** (`config-apply` with a lossless `astconf` edit) instead of `susb tune save`. Pros: history, diff, conflict detection and formatting preservation for free; Asterisk would not rewrite the file. Cons: it would not update the interface EEPROM, needs a `module reload chan_simpleusb.so` to apply (not shown to be glitch-free for a live node with two devices), and needs template-aware editing of values that are inherited from `[node-main](!)`. Re-evaluate after the bench test in section 8 shows how much Asterisk reformats the file.

### 5.4 API sketch

Base path `/api/radio`. Same conventions as the rest of the API (JSON, CSRF header, `authed(role)` wrapper, `audit()` for writes). Roles: **viewer** has no access (the page can reveal device paths and serials); **operator** reads and meters; **admin** changes settings, keys the transmitter and saves. (Rationale: any change affects on-air audio; keying is a regulatory act.)

| Endpoint | Role | Body / response |
|---|---|---|
| `GET /api/radio` | operator | `{available, version, devices:[{name, node, usb:{path,serial,card}, present, active}]}`; `available:false` with `reason` (`no_chan_simpleusb`, `ami_unavailable`, `no_command_permission`, `unsupported_version`) |
| `GET /api/radio/{dev}` | operator | `{dev, params:{rxVoice, txA, txB, rxBoost, preEmphasis, deEmphasis, plFilter, pttInvert, echo, carrierFrom:"no\|usb\|usbinvert\|pp\|ppinvert", ctcssFrom, rxOnDelayFrames, txOffDelayFrames, hw:{micPlayMax, spkrMax, micMax}}, saved:{...}, dirty:["rxVoice",...]}`. `saved` comes from `simpleusb.conf` |
| `PUT /api/radio/{dev}/params` | admin | `{rxVoice?, txA?, txB?, rxBoost?, ...}` partial; applies each field with its command; response `{applied:[{field,value,reply}], errors:[{field,message,reply}]}`. 207-style: partial success is reported per field |
| `GET /api/radio/{dev}/meter` | operator | SSE; events `meter` `{dev, pk, avg, min, max, clip, rxKeyed, ts}`, `error`, and `end` after 5 minutes. At most 3 concurrent streams system-wide; 429 otherwise |
| `POST /api/radio/{dev}/tone` | admin | `{confirm:"<node number typed>", ack:true}`; refuses (409 `node_keyed`, 409 `tone_busy`, 429 `rate_limited`, 409 `channel_not_ready`). Blocks about 5 s and returns `{ok, started, ended, txStatsPeakDbfs?}` (or returns 202 and streams progress on the meter stream; the blocking form is simpler for v1) |
| `POST /api/radio/{dev}/save` | admin | `{}`; response `{saved:true, changed:[{key,from,to}], commit?}`; 409 `save_mismatch` when the file does not reflect the running values afterwards |
| `POST /api/radio/{dev}/revert` | admin | `{}`; re-applies the values read from the file; response as for params |

Rate limits (in addition to the existing per-session limits): parameter writes 10/s per user (sliders), tone 1 per 10 s per device and 6 per 10 minutes per user, meter streams 3 system-wide, save 1 per 5 s. Failed `Command` calls count towards a circuit breaker: after 5 consecutive failures the radio API reports `radio_unavailable` for 30 s instead of hammering Asterisk.

### 5.5 Helper vs main service

| Operation | Owner | Why |
|---|---|---|
| All `susb ...` commands, meter, tone, save | main service (AMI) | AMI `Command`; Asterisk writes its own files; no root needed |
| Reading `simpleusb.conf` for saved values / diff | main service via existing config read path (helper `config-read`) | existing privilege path; the server never reads `/etc/asterisk` directly |
| History record after save | helper (new op `config-record`) | the history repository is root-only |
| Reading AMI credentials | main service (already done) | unchanged |

### 5.6 Frontend notes

- New `web/src/components/Radio.svelte` (lazy import like `Config.svelte`), a small `lib/radio.svelte.ts` store (params, saved, meter stream), types in `lib/types.ts`, mock handlers under `web/mock/` for `npm run dev:mock`, and demo-mode behavior in `internal/demo` (a simulated `susb` handler producing wobbling meter values and canned replies so `make demo` shows the page).
- Reuse `Confirm.svelte`/`Dialog.svelte` for the tone dialog; reuse the toast and form helpers.
- The meter should be a lightweight canvas or CSS bar; no charting library.

## 6. Other considerations

### 6.1 Multi-node on one Pi

A Pi with two nodes may have two `chan_simpleusb` devices, or one SimpleUSB device plus a non-USB node (for example a USRP bridge). Only stanzas in `simpleusb.conf` are tunable. The device list is derived from `menu-support 1` intersected with `rpt.conf` (`rxchannel = SimpleUSB/<dev>`) to show node numbers, and any stanza without a USB device is shown disabled with the driver's "not found" message. Rules: one device is tuned at a time; the selector is explicit; the header line always shows `Tuning node 1999 (device 1999)`; the active device is restored after each operation.

`susb tune swap <other>` swaps the USB interface leases of two devices and is not needed here, but be aware that it exists (an unrelated user could use it from the terminal at any time). The identity line in the page (usb path/serial) helps users notice a swap.

### 6.2 What the page must not do

`rxcap`, `txcap`, `nocap`, `load`, `swap`, `rxd`, `b`, `v`, `y`, `z`, `l`, `fK`, `gK` are never sent. The command builder is a closed set of fixed formats.

### 6.3 `chan_usbradio` (note only)

Same architecture, different names, and a more complex tuning set. CLI: `radio active [name]`, `radio show settings`, `radio tune <fn> [value]` with functions `rxnoise`, `rxvoice`, `rxtone`, `rxsquelch`, `txvoice`, `txtone`, `txall`, `auxvoice`, `txslimsp`, `flash`, `save`, `load`, `menu-support`, and `menu-support` letters (`a` receive tune, `b` receive display, `c` rx voice level, `d` rx ctcss level, `e` squelch, `f` voice tx, `g` aux tx, `h` tx test tone, `i` tune rx, `j` save, ...). Levels 0-999 as well. It writes its settings to its main config (`usbradio.conf`) through the same `tune_write` pattern. Its tuning involves CTCSS decode levels, noise/squelch (`rxnoise`, `rxsquelch`) and TX CTCSS level (`txtone`) which are separate procedures with their own displays. Out of scope for the beta; the `internal/radio` interface should be driver-neutral (a `Driver` with `Prefix`, `Active`, `Params`...) only if this does not complicate v1. The ASL3 default build ships `chan_usbradio` disabled (`noload` in `modules.conf`).

### 6.4 Pre-flight checks before keying or changing TX

Before the tone is allowed: AMI connected, device `present`, node known to app_rpt (it appears in `RptLocalNodes` / `rpt stats`), node not keyed (`RPT_RXKEYED`/`RPT_TXKEYED` false from the existing state), no tone run in progress. If any check fails the button stays disabled with the reason.

## 7. Safety concerns

| Concern | Mitigation |
|---|---|
| Keying a transmitter into an antenna by mistake | Role admin; separate explicit modal with typed node number and dummy-load acknowledgement; fixed ~5 s bound from `susb tune flash`; rate limits; audit log; banner while transmitting. The tone path cannot hold PTT longer than the driver's own bounded loop |
| Over-deviation on air from a high Tx level | Show a warning when Tx A/B changes by more than a configurable step (suggest 100) while the node is keyed; recommend setting levels with the tone on a dummy load or monitored; the UI never ramps automatically |
| RX level and carrier/CTCSS source changes breaking squelch | Changing `carrierfrom` / `ctcssfrom` to `no` makes the receiver carrier-squelch (possibly stuck open); show a confirm with the current value and a "this can cause the repeater/link to key constantly" note; only whitelisted codes are sent |
| Out-of-range `r`/`s` codes (driver array index, 2.7) | Whitelist; never forward numbers from the client |
| Un-saved tuning lost on reload | `Config` page reload after editing `simpleusb.conf`, or a restart, discards live-only values. Show an "unsaved radio tuning" warning in the config apply dialog when `dirty` is non-empty |
| Another operator changing the active device behind our back | Name verification in replies, tune mutex; document the residual race |
| Asterisk rewriting `simpleusb.conf` (formatting, comments, templates) | Diff shown after save; history record via helper; bench test in section 8; fall back to the config-editor pipeline if the rewrite is unacceptable |
| Tone or `Y` on a device with no channel | Pre-flight checks 6.4; suspected NULL-owner path in the driver, test on hardware before enabling the tone button |
| AMI user lacks `command` permission or the connection is shared with others | `GET /api/radio` reports `no_command_permission` with the exact fix text |
| Public exposure | Nothing from this feature goes to the public page; viewer role gets no access; device paths and serials appear only for operator/admin |
| SD-card wear from AMI temp files | Meter capped at 2 Hz, a single poller, auto-stop, 5 minute cap |
| Regulatory | Station identification and license conditions are the operator's responsibility; the confirmation text says so and the tone is never sent without it |

## 8. Open questions

Hardware bench tests (priority, needed before coding the tone and meter):

1. **AMI behavior of the interactive commands.** Confirm on a real node that `menu-support b`, `v`, `Y`, `l`, `fK` over AMI return immediately (regular-file fd is always "readable"), that `Y` returns the stats line, and that `susb tune flash` blocks for about 5 s and keys for about 3 s in total. Confirm `susb tune flash` works when the Command connection goes away mid-run.
2. **Meter calibration.** What `Pk` (dBFS) corresponds to a known reference deviation (for example 1 kHz at 3 kHz deviation on a 5 kHz channel)? The terminal meter marks "3KHz"/"5KHz" at roughly 44% and 67% of its 0-16384 scale (about -13 dBFS and -9.5 dBFS of 16-bit peak by a first calculation from the source; unverified and the marker spacing is not linear in the header string). Measure with a signal generator or service monitor, and check what the ASL3 manual's tuning procedure says (could not fetch it here).
3. **Tone shape.** A 5 s run of 3 bursts is what `flash` gives. Is that enough to measure deviation on a service monitor, or do we need a longer continuous tone? Options: call flash repeatedly (gaps of 1 s remain), or contribute a driver option upstream. What is the correct tone frequency/level (source uses a 1004 Hz tone at 7200 amplitude units; the prompt says 1000 Hz)?
4. **Save reformatting.** How much does `ast_config_text_file_save2` change the ASL3 default template-based `simpleusb.conf` (comments, template inheritance, whitespace alignment of trailing comments)? This decides whether `susb tune save` is acceptable or whether to write via the config pipeline.
5. **Reload semantics.** What does `module reload chan_simpleusb.so` do to a running device (is PTT/audio interrupted, are un-saved values reset)? Determines whether Revert may use it and how loud the "unsaved tuning" warning must be.
6. **Concurrent consumers of the active device.** Does anything else in ASL3 use `simpleusb_active` while a node is running (console key/unkey CLI, `asl-menu` actions, other panels)? If so, is the mutex plus name verification enough, or do we need to gate the page behind an "I am the only operator" notice?
7. **Version support.** `0+4` and `Y` exist in `3.5.0` and `3.10.0`, not in `3.0.0`. Which ASL3 releases do we support (the config doc already asks the same question), and should the page degrade (no meter, no 16-field block, use the 12-field `0`)?
8. **Channel not up.** Behavior of `flash` when the device is configured but app_rpt/the channel is not running (possible crash). Test before enabling the tone.
9. **EEPROM devices.** For `eeprom = 1` interfaces, does Asterisk's EEPROM write finish before the CLI returns? (`tune_write` only sets `eepromctl = 2`; the write happens asynchronously.) Should the UI show "EEPROM write pending" and how can it be verified?
10. **Roles.** Is operator read-only meter access desirable, or should the whole page be admin-only for the beta?
11. **AMI permissions.** Does the default ASL3 AMI user already have `command` write permission? Needed for the first-run diagnostics message.
12. **SA818 / SHARI.** SA818 module settings (frequency, CTCSS/DCS, squelch, filters) are programmed by `sa818-menu` and stored in `/etc/sa818.conf` and the module; nodespot today only backs that file up. Receive/transmit audio levels of the interface are `simpleusb` levels (this feature), but the module-side volume, filters and deviation-related options are a different tool. Should the Radio page later link to or wrap `sa818-menu`, and is that a separate design?

## 9. Phased plan (beta-sized)

Each phase is shippable in a beta and degrades gracefully if the next is not done. The CLAUDE.md working agreement applies: small, well-defined jobs go to the cheapest adequate model; the keying/tone and the helper op are security-relevant and get careful review.

**Phase 0: bench validation (blocking, about 1 day on hardware, no code shipped).** Items 1, 4, 5, 8 of section 8 using a scratch script and a real or spare node; record captured AMI transcripts (anonymised) into `internal/radio/testdata` for parsers and fakes. Decide: `susb tune save` vs the config pipeline; whether the tone ships.

**Phase 1: read-only Radio page (beta N).**
- `internal/radio`: dedicated AMI connection, tune mutex, command builder, parsers for `1`, `active`, `0+4`, `2`, `Y`, with tests against fakes and captured output; demo-mode support.
- `GET /api/radio`, `GET /api/radio/{dev}`, meter SSE (`/meter`), operator role.
- `Radio.svelte`: device list, values from `simpleusb.conf` and running values, dirty indicator, live RX meter.
- Docs: API.md and a short user section.
Value: the operator can watch RX audio levels while adjusting the radio, without any write risk.

**Phase 2: levels and options, save (beta N+1).**
- `PUT /api/radio/{dev}/params` with validation (whitelist for `r`/`s`), sliders and switches, unsaved indicator, `POST /save`, `POST /revert`, audit entries, config-apply "unsaved radio tuning" warning.
- Helper op `config-record`, history entry per save, diff display.
- Per-device selection for multi-node, active-device restore, name verification, rate limits.
Value: full RX/TX level and option tuning, replacing `simpleusb-tune-menu` except for the TX tone.

**Phase 3: TX test tone (beta N+2, only if Phase 0 confirmed the behavior).**
- `POST /tone` with confirmation, preflight checks, rate limits, banner, Z stats, audit.
- Safety review (separate pass) and an operator-facing doc paragraph on dummy loads and ID rules.

**Later (not in beta):** guided wizard with reference-level calibration markers, `chan_usbradio` support, SA818 helper page, per-device "tuning notes" in the nodespot database.

Rough size: Phase 1 and 2 are each a multi-file feature (Go package plus handlers plus Svelte page, roughly a week each including tests); Phase 3 is small in code but gated on hardware results and review.

## Implementation notes (1.1.6)

Code: `internal/susb` (service, parsing, closed command set), `internal/api/handlers_radio.go`, helper op `config-record`, `web/src/components/Tune.svelte`, demo simulator `internal/demo/susb.go`, mock `web/mock/radio.ts`. API: [API.md](API.md#radio-tuning-admin).

Differences from the design above:

- Admin only (not operator). The page is Settings, AllStarLink, Audio levels (`#/settings/allstarlink/audio-levels`; the old `#/tune` link redirects), a tab shown only to admins and only when a SimpleUSB device exists; the page shows an empty state otherwise.
- The meter is polled over REST (`GET /api/radio/{dev}/meter`, about once a second), not SSE. At most one read per 700 ms reaches the driver.
- Revert sets the baseline values (simpleusb.conf laid over the running state) back with the set commands. `susb tune load` is not used: it reads the interface EEPROM, not the file.
- A save whose file does not show a running value, or whose history record fails, is reported as a warning, not an error.
- No Z stats and no circuit breaker. The meter does not restore the previously active device (each operation sets the active device itself and checks the reply).
- The dedicated AMI connection runs with events off; a tone, a save or an echo change holds a per-service lock, a meter read does not wait for it (it returns the last reading).

Links and tone/echo (checked in `chan_simpleusb.c` and `app_rpt`): tone and echo audio is generated inside chan_simpleusb and written to the radio's own transmit queue, so it is not sent to linked nodes. Links hear only the receiver's normal RX audio. During the tone, audio from links is still mixed into the transmitter (the tone is local to the radio TX path); during echo playback, app_rpt TX frames are dropped. `Y` reads the USB capture buffer only, so link audio is not in the meter. The UI therefore shows the link status, warns (does not block) before echo and tone when links are present, names the linked nodes in the tone confirmation, offers "Disconnect all links", and says that permanent or auto-reconnect links may return.

Not verified on hardware (written from the driver source and the bench notes only):

- `susb tune flash` over AMI `Command`: whether the call blocks for the whole tone and how the replies look, and the channel-up pre-flight check against `core show channels concise` (it could refuse a working node).
- Echo mode (`k0`/`k1`) over AMI, including that it really stops on timeout, page exit and service stop.
- `menu-support 0+4`, `Y` and the set commands over AMI, and the reply strings the parsers expect.
- Save: how `susb tune save` rewrites `simpleusb.conf` (formatting, comments, which keys) and the history record; EEPROM behavior.
- Link behavior during a tone and during echo on a node with real links.
- `chan_usbradio` is not supported.

## Appendix: bench results (2026-11-16, SHARI SA818 node, ASL3 on Debian 13)

Both read-only commands work over `asterisk -rx`, so the reference node's ASL3 version has `0+4` and `Y`.

- `susb tune menu-support 0+4` returned `500,0,0,0,1,1,1,0,4,0,0,0,650,31,37,35`. That is 16 fields, parsed with the table in 2.2:
  - Tx A level 500, Tx B level 0.
  - Echo mode off, rxboost off.
  - Pre-emphasis, de-emphasis and PL filter on.
  - invertptt 0.
  - carrierfrom code 4 (usbinvert), ctcssfrom code 0 (no).
  - rxondelay 0, txoffdelay 0.
  - Rx level 650.
  - Mixer maximums 31/37/35.
- `susb tune menu-support Y` with squelch closed returned `RxAudioStats: Pk -76.3  Avg Pwr -89  Min -90  Max -87  dBFS  ClipCnt 0`. That is the idle noise floor, and the format matches 2.4.

- `susb tune menu-support 1` returned a single device name. Only one of the reference node's two nodes has a SimpleUSB radio; the other has none. The UI must therefore list only nodes that have a SimpleUSB device, and handle the common one-device case without a device picker.
- `Y` with normal speech from a handheld into the receiver returned three readings: `Pk -15.6 / -17.5 / -15.8`, `Avg Pwr -34 / -36 / -33`, `Max -27`, `ClipCnt 0`. At rx level 650 that gives about 16 dB of peak headroom with no clipping. Polling once a second gives a stable reading.

Still to test on the bench: whether these commands behave the same through AMI `Command`, calibration of `Y` against a known deviation, and `susb tune flash`.
