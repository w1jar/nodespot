# Replacing Pi-Star with nodespot on an MMDVM hat

Design notes, 2026-11-27. Nothing here is built yet. Protocol details marked "to verify against MMDVM firmware source" are from memory and must be checked before code depends on them.

## Origin

This document supersedes the aslcp plan on branch `try/mmdvm-hybrid` (`docs/PLAN-mmdvm-hybrid.md`). Most of the research there still holds: the Pi-Star gap list, the modem protocol notes, routing and contention, regulatory and safety rules, the test plan and the research list. The architecture does not, because nodespot is no longer an Asterisk add-on. The differences:

- **No Asterisk, USRP or bridge node.** The old plan put the hat behind an Asterisk node and a DV bridge. nodespot has a native node (`nodespot-node`), so the hat becomes the node's radio.
- **No split between an ASL mode and a hotspot mode.** nodespot already has modes (ASL, EchoLink, BrandMeister, DMR, TGIF, YSF, P25, NXDN, M17, Parrot). The hat is a new kind of radio, not a new product mode.
- **No second product.** The old plan sold a separate hotspot product under its own name. Here it is one nodespot install with a different radio preset.
- **No separate DV engine.** The old aslcp-dv engine is now `nodespot-node` plus `internal/native/mode/dv` and `internal/dv/...`, which already speak the networks.
- **Milestones restarted.** The old M0 to M19 list is replaced by the phases below, ordered so the owner's gear can test each one.
- **Dropped:** radio control of the node by DMR private call (it may come back later as an ASL control feature), the engine rename, the release shape and the old product questions, which are all settled.

## Goal

Run nodespot on a Raspberry Pi with an MMDVM hat and use it the way people use Pi-Star: a DMR handheld talks to the hat, the hat talks to the active network. Setup stays the nodespot way: one Station page, modes that apply live, a backup that restores the whole node.

### Scope

- A pure-Go MMDVM serial driver for DMR first, then the other digital modes.
- The hat as a node radio: DMR over RF to and from the existing DV modes (BrandMeister, TGIF, other Homebrew masters).
- Duplex hats with two slots, simplex hats with one.
- Same-codec calls pass AMBE straight through. Cross-mode calls (DMR RF to ASL, EchoLink, YSF and so on) go through the software AMBE vocoder.
- Pi setup done by the installer and the helper.

### Non-goals

- No MMDVMHost, DMRGateway, YSFGateway or any other Pi-Star component (see Decision).
- No firmware builds in nodespot. Flashing, if it ever comes, uses a vendor image the owner supplies.
- No bridging of two networks. The rule stays one active mode per slot, and RF is always the local end.
- No analog FM on the hat. An analog node keeps the SHARI or URI.
- No Pi-Star web UI clone. The features map onto nodespot pages.

## Decision: our own Go driver

**Decided by the owner:** the MMDVM modem driver is written natively in Go inside nodespot. The hotspot has no external project dependencies at runtime or build time: no MMDVMHost, DMRGateway, YSFGateway or other Pi-Star parts. The only external piece is the existing software AMBE vocoder, and only cross-mode calls need it.

The reasons:

- **One binary.** It stays `CGO_ENABLED=0` and ships in the same `.deb` for arm64 and amd64.
- **Nothing extra to install or update.** No C++ toolchain, no second GPL program, no rendered `MMDVM.ini`, and no second sandboxed service.
- **Our own routing.** One state machine in the node owns slots, hang time, timeouts and TX inhibit. The DMR framing already exists in `internal/dv/dmr`, and `internal/native/radio/sa818` shows how to drive a serial port in pure Go (termios via `golang.org/x/sys/unix`).

The MMDVM firmware source and MMDVMHost are protocol references only: we read them to check frame layouts, and we may run them on a bench to compare captures. Neither is ever shipped or called.

## Feature parity with Pi-Star

Status: **have** means nodespot already has it, **M*n*** is the milestone below, **later** is planned with no milestone yet, and **never** is out of scope.

| Pi-Star feature | nodespot status |
| --- | --- |
| DMR to BrandMeister | have (bm mode); RF side M3/M4 |
| DMR to TGIF | have (tgif mode); RF side M3/M4 |
| DMR to other Homebrew masters | have (dmr mode); RF side M3/M4 |
| Duplex hat, two slots | M5 |
| One network per slot (DMRGateway style) | M5 (fixed one network per slot); rewrite rules later |
| DMR talkgroup changes from the radio (group call to a TG) | M4 |
| BrandMeister Parrot (TG 9990) | have; RF test in M4 |
| Last heard, now calling | have; "via RF" tag M3 |
| Dashboard modem status (version, state, overflows) | M2, M6 |
| Modem detection and info | M2 |
| Host files (DMR_Hosts.txt and others) | have (Host lists) |
| Caller lookup | have |
| Station ID | have for analog; DMR carries the ID in the LC (see Regulatory) |
| Backup and restore | have; RF settings M6 |
| Pi-Star backup import | later |
| Cross-mode (DMR RF to ASL, EchoLink, Parrot) | M7 |
| DMR RF to YSF or NXDN through AMBE repack | M8 |
| YSF on RF | M9 |
| NXDN, P25, M17 on RF | later |
| D-Star | later, low priority |
| BrandMeister static talkgroups and API | later |
| POCSAG / DAPNET | never for now |
| Nextion and OLED displays | never for now |
| Firmware flashing from the UI | never for now |
| Wires-X | never (not implemented in the YSF network either) |
| Remote web SSH | have (Terminal, through the helper) |
| WiFi setup | have |
| Expert editors for MMDVM.ini and friends | never (there are no such files) |

## Hardware

### The owner's gear

- **Pi:** Raspberry Pi 3B+.
- **Hat:** one dual-slot (duplex) MMDVM hat. The exact model is unknown; M2 identifies it from the GET_VERSION description string and any board and hardware info it reports, and shows it in Settings > Station > Radio. Which hat it is is something M2 finds out, not a question.
- **Modem firmware:** 1.6.1, as reported. That number looks like the MMDVM_HS hotspot firmware line (the full-size MMDVM repeater firmware uses date versions). If so, it is a hotspot board on the MMDVM_HS firmware, and the protocol version it speaks (1 or 2) decides the GET_VERSION and GET_STATUS layouts. To verify against MMDVM firmware source: which MMDVM_HS release first answered with protocol version 2, and whether 1.6.1 does.
- **Radio:** Anytone AT-D868UV (DMR and analog handheld).

All of the first milestones (M1 to M6) can be tested with that set and a dummy load. The AT-D868UV needs a codeplug with a hotspot channel: the hat's frequency, a color code matching the hat, the slot (slot 2 for simplex use, either for duplex), and a receive group list with the test talkgroups and Parrot (9990). This document never records the owner's frequencies, callsign or IDs; tests use placeholders such as N0CALL and DMR ID 1234567.

### What can't be tested yet

- YSF, NXDN, P25 and M17 on RF: the AT-D868UV is DMR and analog only.
- D-Star on RF: no D-Star radio, and the hat's D-Star support is unknown.
- A second simultaneous DMR station on the other slot: there is one handheld. M5 tests the second slot from the network side and by switching the radio between slots.
- Other hat models, the full-size MMDVM board and other Pi models.

### No-RF alternatives

- **Fake modem:** a Go implementation of the modem side of the serial protocol on a pty or pipe. It ACKs commands, reports version and status, and loops TX frames back as RX when asked. All of M1 runs on it, in CI.
- **Recorded frames:** captures of the serial stream from the real hat (RX from the AT-D868UV, TX of a Parrot reply) saved as test fixtures and replayed through the fake modem.
- **Unit vectors:** the existing `internal/dv/dmr` vectors (checked against MMDVMHost) cover bursts, LC, EMB, slot type and the FEC. New vectors cover the serial framing.
- **Bench reference:** an SD card with Pi-Star on the same Pi and hat, to compare what the hat does under a known-good host. Bench only.

## Where the modem fits in nodespot

### Today

`nodespot-node` has one radio and one active mode. The radio (`internal/native/radio`, interface `radio.Radio`) is analog: it captures and plays 8 kHz PCM in 20 ms `audio.Frame`s and has PTT and COS. Every mode, including the DV modes in `internal/native/mode/dv`, sends and receives PCM. The DV mode wraps a `network.Network` and runs encoder and decoder goroutines, so every DV call is decoded to PCM because the radio is analog. The engine Step runs every 20 ms with the simplex logic: COS and PTT, hang (`advanced.hangMs`), TX timeout (`advanced.txTimeoutS`), AGCs, prebuffer and the receiver priority.

### The hat as a digital radio

The hat is not PCM, so it does not fit `radio.Radio`. It gets a new interface beside it, a digital radio that carries codec frames and call headers per slot, reusing the network types:

```go
// package radio (sketch)
type DigitalRadio interface {
	Slots() int                                  // 1 simplex, 2 duplex
	Run(ctx context.Context, sink network.Sink)  // RF RX: Header, Voice, End per slot
	StartTX(h network.CallHeader) error          // h.Slot picks the slot
	WriteVoice(f network.VoiceFrame) error       // 7-byte AMBE for DMR
	EndTX(slot int) error
	Abort()                                      // stop TX now, all slots
	Status() ModemStatus                         // version, mode, flags, buffer space
	Close() error
}
```

- `network.CallHeader` (Src, SrcID, TG, Slot, Private) and `network.VoiceFrame` (Seq, Codec, Data) are already what every DV network uses, so RF and network calls look the same to the node.
- `ctl.Target` stays the tune target. An RF group call to TG *n* on slot *s* becomes `ctl.Target{TG: n, Slot: s}` for the slot's network.

### Packages

- `internal/native/radio/mmdvm`: the serial protocol (framing, commands, ACK and NAK, status polling, buffer flow control) and `DigitalRadio` for DMR. It uses `internal/dv/dmr` for bursts (`dmr.NewTX` with `MSSync: false` for base-station downlink, `dmr.NewRX` for the handheld's uplink; the sync choice is to verify on air). The serial port reuses the sa818 approach (termios, no CGO).
- `internal/native/radio/mmdvm/fake`: the fake modem.
- `internal/native/rf`: the node's RF endpoint. It owns the slots, routes calls between the `DigitalRadio` and the modes, and runs contention, hang, timeouts and the loop guard.
- A new radio preset, `mmdvm`, next to `shari`, `uri` and `none`. With it the node builds the RF endpoint instead of the PCM radio path.

### Data flow

**Same codec (DMR RF and the bm, tgif or dmr modes).** AMBE goes straight through with no vocoder:

```
handheld -> hat -> mmdvm (burst -> 3x AMBE + LC) -> rf endpoint -> network.Network (DMRD / Rewind)
network.Network -> rf endpoint -> mmdvm (AMBE -> bursts, LC, EMB) -> hat -> handheld
```

This needs a pass-through hook in `mode/dv`: an optional interface the node prefers when the radio is digital and the codec matches:

```go
type FrameMode interface {
	SendHeader(h network.CallHeader) error
	SendFrame(f network.VoiceFrame) error
	EndFrames() error
	RecvFrames() <-chan FrameEvent // header, voice or end, with the slot
}
```

The DV mode then skips its encoder and decoder goroutines and hands frames to `network.Network` (`StartTX`, `WriteVoice`, `EndTX`) and back from its `Sink`.

**Cross-mode (DMR RF and ASL, EchoLink, Parrot, YSF with IMBE, P25, M17).** The RF endpoint decodes RF AMBE to PCM with the software AMBE vocoder and feeds the mode's normal PCM path (`Send`, `Recv`). The other way, mode PCM is encoded to AMBE and sent as DMR. Without the vocoder installed, cross-mode is not offered and the UI shows the existing vocoder banner.

**Repack (later).** YSF DN and NXDN also carry AMBE, so a DMR to YSF or NXDN call could repack the codewords without the vocoder. That is M8.

### Modes and slots

The node rule is one active mode. A simplex hat keeps it: one slot, one mode. A duplex hat bends it to **one active network per slot**: slot 1 can be linked to one network and slot 2 to another, each with its own target. Networks are still never bridged with each other; each slot bridges RF to its own network only. Until M5 the duplex hat uses one slot (slot 2 by default, matching the Homebrew client and TGIF).

Notes on the networks:

- The Homebrew client (`internal/dv/network/homebrew`) transmits on one slot (option `slot`, default 2, always 2 for TGIF). Per-slot networks need one client per slot, or a client change for both slots on one login. To decide in M5.
- BrandMeister in nodespot uses Rewind as an Open DMR Terminal, which has no slot of its own (to verify). Mapping it to an RF slot is the RF endpoint's job. A Homebrew login to a BrandMeister master with a hotspot password is the fallback if Rewind is a poor fit for RF use.

## MMDVM serial protocol, first subset

Everything in this section is to verify against MMDVM firmware source (MMDVM and MMDVM_HS `SerialPort.cpp`) and MMDVMHost `Modem.cpp`.

### Link

- UART at 115200 baud, 8N1, no flow control (to verify; some boards use 460800).
- The modem only speaks when spoken to, except for unsolicited RX data and debug messages.

### Frame layout

```
byte 0     0xE0              frame start
byte 1     length            whole frame, including these 3 bytes
byte 2     command
byte 3..   payload
```

Frames longer than 255 bytes use a two-byte length in later firmware (to verify; DMR frames never need it).

### Commands needed first

| Command | Value | Direction | Payload |
| --- | --- | --- | --- |
| GET_VERSION | 0x00 | host to modem, reply | Reply: protocol version, then (v1) a description string, or (v2) capabilities, CPU type, a 16-byte unique ID and the description |
| GET_STATUS | 0x01 | host to modem, reply | Reply: enabled modes, current mode, flags (TX on, ADC overflow, RX overflow, TX overflow, lockout, DAC overflow, carrier detect), then free buffer space per mode, with DMR slot 1 and slot 2 separate. Layout differs between v1 and v2 |
| SET_CONFIG | 0x02 | host to modem, ACK | Flags (RX and TX invert, PTT invert, debug), enabled modes, TX delay, initial mode, RX and TX levels, DMR color code, DMR delay, offsets and per-mode levels. Long and version specific; build it from firmware source, not memory |
| SET_MODE | 0x03 | host to modem, ACK | Mode byte: idle 0, D-Star 1, DMR 2, YSF 3, P25 4, NXDN 5, POCSAG 6, M17 7 |
| SET_FREQ | 0x04 | host to modem, ACK | RX and TX frequency in Hz (32-bit little endian each), RF power level, then optional POCSAG frequency. Hotspot boards only |
| DMR_DATA1 / DMR_DATA2 | 0x18 / 0x1A | both | 1 byte of control (frame type and data type or voice sequence, sync flags) plus the 33-byte burst |
| DMR_LOST1 / DMR_LOST2 | 0x19 / 0x1B | modem to host | Sync lost on that slot; the call ended without a terminator |
| DMR_SHORTLC | 0x1C | host to modem, ACK | 9 bytes of short LC for the CACH, telling radios which slots are busy (duplex only) |
| DMR_START | 0x1D | host to modem, ACK | 1 byte: 1 starts the duplex TDMA downlink, 0 stops it |
| DMR_ABORT | 0x1E | host to modem, ACK | 1 byte slot number: drop that slot's queued TX |
| ACK | 0x70 | modem to host | The acknowledged command byte |
| NAK | 0x7F | modem to host | The command byte and a reason code |
| DEBUG1 to DEBUG5 | 0xF1 to 0xF5 | modem to host | Text and values; logged, never parsed |

The 33-byte DMR burst inside DMR_DATA is the same burst `internal/dv/dmr` builds and parses (`dmr.Burst`), so no new air-interface code is needed for DMR.

### Duplex slot handling

- On a duplex hat the host sends DMR_START 1 before the first TX and DMR_START 0 after the last slot goes idle plus the hang time. While started, the modem sends the TDMA downlink continuously and fills an idle slot itself (to verify that the firmware, not the host, sends idle bursts).
- The host sends DMR_SHORTLC whenever a slot changes between idle, group and private, so radios see which slot is busy.
- Each slot has its own TX queue (DMR_DATA1, DMR_DATA2) and its own free-space count in GET_STATUS.
- A simplex hat (duplex off in SET_CONFIG) uses slot 2 frames only and no DMR_START; the radio must be set to the same slot and color code (to verify which slot number the firmware expects for simplex).

### Timing

- A DMR burst covers 30 ms of air time per slot, one burst every 60 ms per slot; a voice superframe is 6 bursts (360 ms) carrying 18 AMBE frames of 20 ms.
- The modem buffers TX. The host keeps a small lead (about 2 to 3 bursts per slot), polls GET_STATUS about every 250 ms when idle and more often during TX, and never writes more than the reported free space.
- Network jitter is absorbed in the RF endpoint, not the modem: it waits for a short prebuffer before the header goes out and fills gaps with `dmr.SilenceAMBE`.
- The host reads continuously. A missed ACK after about 1 s means the modem is gone: abort, close and reopen.

### Start-up sequence

1. Open the UART and drain it.
2. GET_VERSION, retrying for about 2 s (the modem may still be booting after a reset). Record the protocol version, description and hardware info.
3. SET_FREQ (hotspot boards), then SET_CONFIG with DMR only and TX off unless TX is allowed (see Regulatory), then SET_MODE DMR.
4. Poll GET_STATUS.

## Routing and contention

- **One network per slot.** Each slot has at most one linked network (one mode until M5). An RF call on a slot goes only to that slot's network; a network call goes only to its slot.
- **First come, first served, RF wins ties.** If a slot is idle, the first call (RF or network) takes it. While a local station transmits on a slot, network audio for that slot is dropped, matching today's receiver priority. A network call already being transmitted is not cut by an RF key-up on the same slot (the handheld cannot hear it then anyway); the RF call is dropped and logged as busy.
- **Hang time.** After a call ends the slot stays reserved for the same source for `advanced.hangMs` (default 500 ms), with the slot shown busy in the short LC. A DMR-only hang option may be needed later; Pi-Star users expect several seconds.
- **Timeouts.** An RF call longer than `advanced.txTimeoutS` (default 180 s) is cut on the network side. A network call longer than the TX timeout is cut on RF and the slot is held off until the network call ends. Missing frames for 300 ms (the DV `DefaultTxTimeout`) end a call; DMR_LOST ends an RF call at once.
- **Loop prevention.** Calls carry a source tag (rf or net) and the source ID. Frames the node just sent to RF are never taken back from RF; a network call whose source ID matches an RF call that ended within 2 s on the same slot is dropped. Two slots are never linked to the same network target.
- **TX and RX priority.** On a duplex hat RX and TX run at once on different slots. On a simplex hat, RX always beats TX.
- **Mode switching.** Selecting a mode applies to the default slot (slot 2) and waits for that slot's current call to end, up to the hang time, then switches. A switch from the radio (group call to a talkgroup) tunes the slot's network to that talkgroup, as Pi-Star users expect.
- **Schedules.** Schedules work through the Linker (Connect, Disconnect, Linked), which selects a mode. On the hat they act on the default slot, unchanged. Per-slot schedules are later. A manual change pauses or cancels the schedule as today.
- **Now calling and Last heard.** RF talkers show with a "via RF" tag and the slot (`ctl.Talk` has a `Slot` field). Network talkers keep their source.

## Pi setup

The installer and the helper do the root work. `nodespot-node` stays a sandboxed user service.

### UART on the Pi 3B+

The 3B+ has two UARTs: the full PL011 and the mini UART, whose baud rate follows the core clock. By default Bluetooth has the PL011 and the GPIO pins get the mini UART, which is unreliable for the modem. The hat needs the PL011 on GPIO 14 and 15.

- `enable_uart=1` in `config.txt`.
- `dtoverlay=disable-bt` to give the PL011 to the GPIO pins and turn Bluetooth off, or `dtoverlay=miniuart-bt` to keep Bluetooth on the mini UART (exact overlay names, and the old `pi3-` prefixed names on older images, to verify).
- `systemctl disable hciuart` when Bluetooth is off.
- Remove `console=serial0,115200` from `cmdline.txt` so the kernel console does not talk to the modem.
- A reboot is needed. The UI says so and offers it.
- Then `/dev/serial0` links to `/dev/ttyAMA0` (to verify on the current Raspberry Pi OS).

### Device access

- A udev rule in `packaging/udev/60-nodespot-node.rules` gives `ttyAMA0` group `nodespot-node`.
- `nodespot-node.service` gets `DeviceAllow=char-ttyAMA rw` (major name to verify; some kernels name the PL011 under `ttyS` or another class). Today it allows `char-alsa`, `char-hidraw` and `char-ttyUSB`. USB hats and boards (for example an MMDVM on a USB serial adapter) are already covered by `char-ttyUSB`, plus `char-ttyACM` for STM32 USB boards.

### GPIO reset

Many hats wire the STM32 reset and boot pins to Pi GPIO (pins vary by board, to verify per board). A helper op pulses reset to recover a stuck modem. The node never touches GPIO itself.

### Firmware

- Detection only: GET_VERSION gives the protocol version, the firmware version and the board description, shown in Settings > Station > Radio.
- No flashing in nodespot for now. If the firmware is too old for the driver, the UI says which minimum is needed. Flashing could come later as a helper op that runs a vendor image through the STM32 serial bootloader, never by default.

### Helper ops

| Op | Does |
| --- | --- |
| `rf-prepare` | Edits `config.txt` and `cmdline.txt` as above, disables `hciuart`, reports whether a reboot is needed. Idempotent. Refuses on non-Pi hardware |
| `rf-device-check` | Reports the UART state, the overlay, whether the console is on it and the device permissions |
| `rf-reset` | Pulses the modem reset GPIO for the chosen board |

The installer does not run `rf-prepare` on its own; the user picks the `mmdvm` preset and the UI asks. The Pi 3B+ runs 64-bit Raspberry Pi OS, which `install.sh` already requires.

## Config and UI

### Settings

All RF settings live in `node.json` under `station.radio` when the preset is `mmdvm`:

| Setting | Notes |
| --- | --- |
| Preset `mmdvm` and the serial port | Detected; `/dev/serial0` by default |
| RX and TX frequency | Validated against the band table (see Regulatory) |
| Offsets | RX and TX offset in Hz, for hats that need trimming |
| RX and TX level, TX delay | Advanced |
| RF power | Hotspot boards only; low default |
| Duplex | From the board, overridable |
| Color code | 0 to 15, default 1 |
| Slots | Default slot for simplex use and mode selection |
| DMR ID and ESSID | DMR ID from Station; ESSID (two digits, 01 to 99) makes the repeater ID |
| Network per slot | M5: which mode and target each slot links to |
| Allow transmit | Off by default; see Regulatory |

### Pages

- **Settings > Station > Station:** callsign, DMR ID (already there), plus the ESSID.
- **Settings > Station > Radio:** the `mmdvm` preset; the modem card (board, firmware, protocol version, state, overflows); frequencies, offsets, power, color code, duplex and slots; the Allow transmit switch; "Prepare the Pi" (runs `rf-prepare`) and "Reset modem".
- **Modes page:** the existing BrandMeister, TGIF and DMR settings. With a duplex hat, a slot choice per mode (M5). Cross-mode modes (ASL, EchoLink, YSF, Parrot) show whether the software AMBE vocoder is installed.
- **Dashboard:** per-slot status (idle, RF RX, network TX, hang), the linked network and target per slot, and Now calling and Last heard with "via RF".

### API

- `GET /api/radio/modem`: board, firmware, protocol version, state, flags, buffer space.
- `POST /api/radio/modem/reset` and `POST /api/radio/prepare`: through the helper.
- `/api/station` and `/api/modes/{mode}` gain the RF fields (ESSID, slot per mode). Document them in `docs/API.md` when built.

### Backup and restore

- RF settings are part of `node.json`, so they are already in the backup.
- After a restore, Allow transmit is forced off until the user turns it on again on that Pi.
- `rf-prepare` state is not in the backup; restoring to a new Pi asks to prepare it.

### Pi-Star backup import (later)

Read a Pi-Star backup zip: the MMDVMHost config for frequencies, color code, duplex, offsets and power; the DMRGateway config for which network is on which slot; and the BrandMeister and TGIF settings. Map them into the Station page and modes and show a preview before apply, the same way as a nodespot backup. Passwords are optional, as in our backups. Allow transmit stays off after import.

## Regulatory and safety

- **TX inhibit until configured.** The modem is configured with TX off until the callsign, DMR ID, frequencies and the Allow transmit switch are all set. Allow transmit is a separate switch, off on a new install and forced off after restore, Pi-Star import and any upgrade that changes the RF settings format.
- **Frequency plan checks.** A built-in band table (the amateur bands the hat can cover, typically 2 m and 70 cm) rejects frequencies outside them. A warning, not a block, for frequencies inside satellite subbands and other segments where hotspots are discouraged. Region differences are an open item; the table is a sanity check, not legal advice.
- **Timeouts.** The TX timeout is hard: network audio stops going to RF after `advanced.txTimeoutS`. A watchdog in the RF endpoint aborts TX if no frame has arrived from the network for 300 ms.
- **Abort on failure.** On SIGTERM, a panic, a lost network or a serial error, the driver sends DMR_ABORT for both slots and DMR_START 0, and drops to idle mode.
- **Low power.** The default RF power is the lowest the board offers.
- **ID.** DMR transmissions carry the source DMR ID in the LC for every call. Whether a CW ID is also needed on RF, as on analog, depends on the region and is a decision for the owner; the analog station ID stays as it is for analog radios.
- **No unattended TX on setup.** Test tones and calibration never key up without a dummy load warning.

## Milestones

Each milestone ends with a test on the owner's gear (Pi 3B+, the dual-slot hat, the AT-D868UV) or on no hardware. M1 and M2 can start right away.

### M1: fake modem and RF endpoint (no hardware)

- `radio/mmdvm` framing, commands, ACK and NAK, status parsing for protocol 1 and 2.
- The fake modem on a pty.
- `DigitalRadio`, the `rf` endpoint, the `FrameMode` hook in `mode/dv`, contention, hang, timeouts and the loop guard.
- **Test:** unit tests plus an end-to-end test in CI: fake modem RX burst stream to a fake network and back, with AMBE bytes compared exactly; two calls on one slot (busy); RF wins; timeout; loop guard; abort on context cancel.

### M2: detect the hat, read version and status

- Helper ops `rf-prepare` and `rf-device-check`, the udev rule and `DeviceAllow`.
- GET_VERSION and GET_STATUS on the real hat; the board is identified from the description string and hardware info.
- The modem card in Settings > Station > Radio.
- **Test:** on the Pi 3B+: prepare, reboot, see the board name, firmware 1.6.1 and its protocol version, mode and flags in the UI. Record the serial stream as a fixture. This settles which hat it is and whether it speaks protocol 1 or 2.

### M3: DMR RX on one slot

- SET_FREQ, SET_CONFIG and SET_MODE with TX off; RF RX to the slot's network.
- **Test:** AT-D868UV on the hotspot channel (placeholder frequency, matching color code, slot 2), group call to a test talkgroup. The call reaches BrandMeister or TGIF (seen on the network's own last-heard page) and shows in nodespot's Last heard as N0CALL 1234567 via RF. Record RX fixtures.

### M4: DMR TX

- Allow transmit, TX path, timeouts, abort.
- **Test:** into a dummy load, lowest power, AT-D868UV close by. Private call to Parrot 9990; the reply plays on the handheld. Then a group call from the network side to a talkgroup in the handheld's receive group list. Check the TX timeout and abort with a long call and by stopping the service mid-call.

### M5: second slot

- DMR_START and short LC; per-slot routing; one network per slot.
- **Test:** slot 1 linked to one network, slot 2 to another (for example TGIF on slot 2 and BrandMeister on slot 1). Network traffic on both slots at once heard by switching the handheld between slot channels; RF on one slot while the other plays.

### M6: UI, Dashboard, API and backup

- Per-slot dashboard, modem card, API, backup and restore with Allow transmit forced off.
- **Test:** back up, restore on the same Pi, see TX off, turn it on, run the M4 Parrot test again.

### Later

- **M7 cross-mode:** DMR RF to ASL, EchoLink and Parrot through the software AMBE vocoder. Test: handheld to Parrot and to an ASL test node (placeholder node 1999).
- **M8 AMBE repack:** DMR RF to YSF and NXDN without the vocoder.
- **M9 and later:** YSF, NXDN, P25 and M17 on RF (needs another radio), Pi-Star backup import, BrandMeister static talkgroups, DMRGateway-style rewrite rules, D-Star, then perhaps POCSAG and displays.

## Test plan

- **Unit:** serial framing round trips, every command builder against hand-checked bytes, GET_VERSION and GET_STATUS for protocol 1 and 2, the DMR_DATA control byte, and the existing `internal/dv/dmr` vectors.
- **Fake modem:** RX, TX, busy, hang, timeouts, loop guard, flow control against reported free space, a modem that stops answering, NAK handling.
- **Fixtures:** serial captures recorded in M2 to M4 replayed in CI. Fixtures are cleaned of IDs and callsigns (replaced with 1234567 and N0CALL) before commit.
- **On air (owner's gear):** the M2 to M6 tests above, always into a dummy load at low power, with the AT-D868UV codeplug set to the hotspot channel.
- **Bench comparison:** the same hat under Pi-Star, for captures when our results differ. Bench only.
- **Regression:** analog presets (`shari`, `uri`, `none`) unchanged; DV modes still work with PCM radios.

## Research list (to verify)

- MMDVM and MMDVM_HS firmware source: command values, SET_CONFIG layout, GET_VERSION and GET_STATUS for protocol 1 and 2, DMR_DATA control byte, frame length rules.
- Which MMDVM_HS release introduced protocol version 2, and what 1.6.1 speaks.
- Whether the firmware or the host fills idle duplex slots, and the simplex slot number.
- UART speed for hotspot boards.
- BS versus MS sync on the downlink and uplink for hotspots.
- Raspberry Pi OS overlay names (`disable-bt`, `miniuart-bt`), the `serial0` link and the device class name for `DeviceAllow`.
- Reset and boot GPIO pins for common hats.
- Whether Rewind can serve an RF hotspot well, or a Homebrew login to BrandMeister is better.
- Band table per region.

## Owner decisions

1. **Networks:** all of the DMR networks nodespot already has: BrandMeister, TGIF and DMR - Other (Homebrew masters, including DMR+). Each slot can be linked to any of them.
2. **Duplex:** the dual-slot hat runs duplex, with both slots in use. Simplex support can come later for single-slot hats.

Smaller design choices (a CW ID on RF, a separate DMR hang time) are noted where they arise and decided when the milestone is built.
