# Replacing Pi-Star with nodespot on an MMDVM hat

Design notes, 2026-11-27. Nothing here is built yet. The MMDVM serial protocol section was checked against the MMDVMHost, MMDVM and MMDVM_HS sources. Remaining open items are listed in the Research list.

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
| Duplex hat, two slots | M4 (a duplex hat hears nothing until it transmits) and M5 |
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
- **Hat:** one dual-slot (duplex) MMDVM hat. M2 read it: GET_VERSION describes it as an `MMDVM_HS` board with dual ADF7021 RF chips and a 14.7456 MHz TCXO, so it can run duplex. The description string carries no other hardware info; the board is named from that string alone.
- **Modem firmware:** 1.6.1, protocol version 1 (the 14-byte GET_STATUS layout), from a Pi-Star-fork build dated 2023-11-15. It answered at 115200 baud on `/dev/serial0`. Upstream MMDVM_HS tops out at 1.6.0, so 1.6.1 is that fork's build. The driver supports protocols 1 and 2.
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
	Run(ctx context.Context, sink network.Sink) error // RF RX: Header, Voice, End per slot
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

- `internal/native/radio/mmdvm`: the serial protocol (framing, commands, ACK and NAK, status polling, buffer flow control) and `DigitalRadio` for DMR. It uses `internal/dv/dmr` for bursts (`dmr.NewTX` with `MSSync: false` for a duplex downlink, `dmr.NewRX` for the handheld's uplink; BS-sourced sync for a duplex hat's downlink and MS-sourced sync for a simplex hat; the uplink is always MS-sourced). The serial port reuses the sa818 approach (termios, no CGO).
- `internal/native/radio/mmdvm/fake`: the fake modem.
- `internal/native/rf`: the node's RF endpoint. It owns the slots, routes calls between the `DigitalRadio` and the modes, and runs contention, hang, timeouts and the loop guard.
- A new radio preset, `mmdvm`, next to `shari`, `uri` and `none`. With it the node builds the RF endpoint instead of the PCM radio path.

### Data flow

**Same codec (DMR RF and the bm, tgif or dmr modes).** AMBE goes straight through with no vocoder:

```
handheld -> hat -> mmdvm (burst -> 3x AMBE + LC) -> rf endpoint -> network.Network (DMRD / Rewind)
network.Network -> rf endpoint -> mmdvm (AMBE -> bursts, LC, EMB) -> hat -> handheld
```

This needs a pass-through hook in `mode/dv`: an optional interface the node prefers when the radio is digital and the codec matches. It is declared in `internal/native/mode` (`mode.FrameMode`, a `Mode` with these methods) so the RF endpoint does not depend on `mode/dv`; `*dv.Mode` implements it:

```go
type FrameMode interface {
	Mode
	FrameCodec() (network.Codec, bool) // the network's codec, once started
	SetFrames(on bool)                 // received calls as frames (RecvFrames) or decoded audio (Recv)
	SendHeader(h network.CallHeader) error
	SendFrame(f network.VoiceFrame) error
	EndFrames() error
	RecvFrames() <-chan FrameEvent // header, voice or end
}
```

The DV mode then skips its encoder and decoder goroutines and hands frames to `network.Network` (`StartTX`, `WriteVoice`, `EndTX`) and back from its `Sink`.

**Cross-mode (DMR RF and ASL, EchoLink, Parrot, YSF with IMBE, P25, M17).** The RF endpoint decodes RF AMBE to PCM with the software AMBE vocoder and feeds the mode's normal PCM path (`Send`, `Recv`). The other way, mode PCM is encoded to AMBE and sent as DMR. Without the vocoder installed, cross-mode is not offered and the UI shows the existing vocoder banner.

**Repack (later).** YSF DN and NXDN also carry AMBE, so a DMR to YSF or NXDN call could repack the codewords without the vocoder. That is M8.

### Modes and slots

The node rule is one active mode. A simplex hat keeps it: one slot, one mode. A duplex hat bends it to **one active network per slot**: slot 1 can be linked to one network and slot 2 to another, each with its own target. Networks are still never bridged with each other; each slot bridges RF to its own network only. Until M5 the duplex hat uses one slot (slot 2 by default, matching the Homebrew client and TGIF).

Notes on the networks:

- The Homebrew client (`internal/dv/network/homebrew`) transmits on one slot (option `slot`, default 2, always 2 for TGIF). Per-slot networks need one client per slot, or a client change for both slots on one login. To decide in M5.
- BrandMeister in nodespot uses Rewind as an Open DMR Terminal, which has no slot of its own (still to verify). Mapping it to an RF slot is the RF endpoint's job. A Homebrew login to a BrandMeister master with a hotspot password is the fallback if Rewind is a poor fit for RF use.

## MMDVM serial protocol, first subset

Checked against the MMDVM and MMDVM_HS `SerialPort.cpp` and MMDVMHost `Modem.cpp`. Byte layouts and source citations are in the implementer's notes kept with the design work.

### Link

- UART at 115200 baud, 8N1, no flow control. MMDVM_HS and hats are fixed at 115200; full MMDVM repeater boards default to 460800, so the speed is a setting.
- The modem only speaks when spoken to, except for unsolicited RX data and debug messages.

### Frame layout

```
byte 0     0xE0              frame start
byte 1     length            whole frame, including these 3 bytes
byte 2     command
byte 3..   payload
```

Frames of 256 bytes or more use a long form: `E0 00 L cmd payload`, with total length L + 255 and a 4-byte header. MMDVM_HS does not support it, and DMR frames never need it.

### Commands needed first

| Command | Value | Direction | Payload |
| --- | --- | --- | --- |
| GET_VERSION | 0x00 | host to modem, reply | Reply v1 (MMDVM_HS): protocol byte 1, then the description string. Reply v2 (MMDVM): protocol byte 2, capability bytes, CPU type, 16 bytes of unique ID, then the description |
| GET_STATUS | 0x01 | host to modem, reply | Reply v1 is 14 bytes: enabled modes, current mode, flags, then free space per mode (D-Star, DMR slot 1, DMR slot 2, YSF, P25, NXDN, POCSAG, M17). Reply v2 is 20 bytes: current mode, flags (TX, ADC overflow, RX overflow, TX overflow, lockout, DAC overflow, carrier detect), then free space per mode. Space is in whole frames |
| SET_CONFIG | 0x02 | host to modem, ACK | v1 is 26 bytes, v2 is 40. Flags (RX, TX and PTT invert, debug, simplex), enabled modes, TX delay in 10 ms units, initial mode, levels, DMR color code (0 to 15), DMR delay, DC offsets, hang times. The offsets are in the implementer's notes. NAK 4 if TX delay is over 50 or the initial mode is not enabled |
| SET_MODE | 0x03 | host to modem, ACK | Mode byte: idle 0, D-Star 1, DMR 2, YSF 3, P25 4, NXDN 5, POCSAG 6, FM 10. The host does not send it at start-up; SET_CONFIG carries the initial mode, and the firmware switches to DMR itself when DMR data arrives |
| SET_FREQ | 0x04 | host to modem, ACK | 17 bytes: a reserved zero, RX and TX frequency in Hz (32-bit little endian each), RF power (0 to 255), POCSAG frequency. Hotspot boards only; the full MMDVM firmware ACKs and ignores it. MMDVM_HS range-checks it, see SET_FREQ rules below |
| DMR_DATA1 / DMR_DATA2 | 0x18 / 0x1A | both | 1 control byte plus the 33-byte burst (frame length 37). Modem to host, the control byte is 0x20 for voice sync, 1 to 5 for voice bursts B to F, and 0x40 plus the data type for data sync (terminator is 0x42). Host to modem, the firmware ignores it and the host sends 0x00. No ACK on success; NAK 5 when the FIFO is full |
| DMR_LOST1 / DMR_LOST2 | 0x19 / 0x1B | modem to host | Sync lost on that slot (after 13 lost frames); the call ended without a terminator |
| DMR_SHORTLC | 0x1C | host to modem | 9 bytes of short LC for the CACH, telling radios which slots are busy (duplex only). No ACK on success |
| DMR_START | 0x1D | host to modem | 1 byte: 1 starts the duplex TDMA downlink, 0 stops it. No ACK on success; NAK 4 if the modem is not in DMR mode |
| DMR_ABORT | 0x1E | host to modem | 1 byte slot number (1 or 2): drop that slot's queued TX. No ACK on success |
| ACK | 0x70 | modem to host | The acknowledged command byte. Sent only for SET_CONFIG, SET_MODE, SET_FREQ and calibration |
| NAK | 0x7F | modem to host | The command byte and a reason: 1 unknown command, 2 wrong state, 4 bad length or data, 5 no FIFO space, 6 duplex not supported by this build |
| DEBUG1 to DEBUG5 | 0xF1 to 0xF5 | modem to host | Text and values; logged, never parsed |

#### SET_FREQ rules (verified on a dual ADF7021 hat, firmware 1.6.1)

A SET_FREQ with a bad frequency is NAKed with reason 4 (bad length or data). The rules are in the MMDVM_HS source, `CIO::setFreq` and `CIO::checkZUMspot` in `IO.cpp` (band and ban limits in `IO.h`, payload parsing in `CSerialPort::setFreq` in `SerialPort.cpp`):

- **Bands.** In 144 to 148 MHz, 219 to 225 MHz, 420 to 475 MHz or 842 to 950 MHz, minimum inclusive, maximum exclusive. RX and TX pass if either one is in a band. The POCSAG frequency must itself be in a band.
- **Banned ranges.** 145.8 to 146.0 MHz and 435 to 438 MHz, both ends inclusive. RX, TX and the POCSAG frequency must all be outside them.
- **POCSAG field is checked, and 0 is refused.** The 17-byte form always carries it, so a host that has no POCSAG frequency must still send a valid one. nodespot sends TX there when `Freq.POCSAG` is 0.
- **Short form.** If the POCSAG field is omitted, the firmware uses TX as the POCSAG frequency. nodespot always sends the 17-byte form.
- **Duplex board.** On a dual ADF7021 duplex board (not dual band), RX or TX below 420 MHz is also refused, so only UHF works. A dual-band board takes VHF or UHF.
- **As built.** `mmdvm.SetFreq` sends TX in the POCSAG field when `Freq.POCSAG` is 0. The fake modem (`internal/native/radio/mmdvm/fake`) applies these checks by default and NAKs reason 4 like the hat (`Options.AnyFrequency` turns them off, `Options.DualBand` allows VHF); its tests use placeholder UHF frequencies outside the banned range. The node's own sanity range (`MinMMDVMFreqHz` to `MaxMMDVMFreqHz`, 100 to 1000 MHz) is still checked first. M3 added a frequency-check helper, `mmdvm.CheckFreq(rx, tx, FreqRules)` (`freqcheck.go`), which says in plain words why the firmware would NAK: a banned range, a TX frequency in no band (the POCSAG field carries TX), or, with `UHFOnly`, a frequency below 420 MHz. `mmdvm.Accepts` is the firmware's own test, and the fake modem uses it, so a grid test keeps `CheckFreq` and the fake in step. The node applies `UHFOnly` when the `duplex` setting is on (the default, for a dual-chip hat), and saving settings with a frequency the helper rejects fails with that message. A dual-chip hat set to simplex with a VHF frequency passes the check and is still NAKed by the modem; the modem card then shows the NAK with the band hint. The same rules are mirrored in `web/src/lib/mmdvm.ts` and shown in the settings form as messages only. The helper pre-checks nothing on the wire: the driver still sends what the node tells it, and a NAK of SET_FREQ is reported with the band hint.

The 33-byte DMR burst inside DMR_DATA is the same burst `internal/dv/dmr` builds and parses (`dmr.Burst`), so no new air-interface code is needed for DMR.

### Duplex slot handling

- On a duplex hat the host sends DMR_START 1 before the first TX and DMR_START 0 after the last slot goes idle plus the hang time. While started, the modem sends the TDMA downlink continuously and fills an idle slot itself with an idle data burst carrying the color code. The host never sends idle bursts. The host also stops the stream by itself after its DMR TX hang timer (4 seconds in MMDVMHost).
- The host sends DMR_SHORTLC whenever a slot changes between idle, group and private, so radios see which slot is busy. (M1 has the command builder only; the driver sends it from M5.)
- Each slot has its own TX queue (DMR_DATA1, DMR_DATA2) and its own free-space count in GET_STATUS.
- A simplex hat (simplex flag in SET_CONFIG) uses DMR_DATA2 only, whatever the call's slot, and no DMR_START or DMR_SHORTLC. RX is also reported on DMR_DATA2, GET_STATUS shows slot 1 space as a fixed 10, and the host must send MS-sourced sync (a duplex hat takes BS-sourced).

#### Receive: duplex versus simplex (verified on the owner's hat)

The firmware decides what it reports to the host from the duplex bit of SET_CONFIG, and the two cases differ a great deal:

- **Duplex, hat not transmitting: nothing is received from a handheld.** With the duplex bit set, the main loop (`MMDVM_HS.cpp`, `loop()`) runs `CDMRIdleRX` while the hat is idle (`CIO::process()` in `IO.cpp` selects by state), and `CDMRIdleRX` (`DMRIdleRX.cpp`) reports only a data-sync CSBK wake-up that is MS-sourced and carries a matching color code. A voice call is decoded by `CDMRRX` only while the hat is transmitting its own downlink (DMR_START 1 and the TDMA stream running). So a duplex hat receives voice only after the host has answered the radio's wake-up by starting TX.
- **Simplex: `CDMRDMORX` runs all the time.** With the simplex bit (0x80 in the SET_CONFIG flags byte) the main loop runs `CDMRDMORX`, which decodes MS-sourced voice, data and terminators at any time and reports them on DMR_DATA2, slot 2 only.

Verified on the owner's dual ADF7021 hat, firmware 1.6.1, protocol 1: in duplex with the hat not transmitting, a handheld's call produced nothing on the serial port. In simplex a group call (placeholders: source 1234567, talkgroup 1) from the handheld on slot 2 reported:

- 3 voice LC headers (control byte 0x41);
- voice bursts with control 0x20 (voice sync, burst A), then 0x01 to 0x05 (bursts B to F), repeating;
- a terminator with LC (control 0x42);
- 118 bursts in 7.0 s, 60 ms apart;
- 0 DMR_LOST, 0 RX buffer overflows, and 241 of 241 GET_STATUS requests answered;
- at most 5 FEC bit errors in a burst, and the embedded LC decoded as a group call, FLCO 0, FID 0, service options 0.

That is the exact shape `internal/dv/dmr` already produces and parses, so no new air-interface code was needed. The fake modem's `PlayCall` (`fake/rx.go`) replays a call in this shape, and like the firmware it hears nothing in duplex unless the host is transmitting.

### Timing

- A DMR burst covers 30 ms of air time per slot, one burst every 60 ms per slot; a voice superframe is 6 bursts (360 ms) carrying 18 AMBE frames of 20 ms.
- The modem buffers TX. The host polls GET_STATUS every 250 ms (as MMDVMHost does), writes at most one frame per mode every 10 ms, and only while the reported free space is above 1, counting it down per frame written until the next status reply. A full FIFO gives NAK 5. nodespot also subtracts the frames written after a GET_STATUS was sent from the space its reply reports, so a reply in flight cannot overstate the space.
- Network jitter is absorbed in the RF endpoint, not the modem: it waits for a short prebuffer before the header goes out and fills gaps with `dmr.SilenceAMBE`.
- The host reads continuously. MMDVMHost waits for an ACK or NAK for up to 30 reads of 10 ms. If no status reply arrives for 2 seconds, the modem is treated as gone: close and reopen.

### Start-up sequence

1. Open the UART and drain it.
2. Wait about 2 s, then GET_VERSION, with up to 6 attempts spaced 1.5 s apart (the modem may still be booting after a reset). Record the protocol version, description and hardware info.
3. SET_FREQ (hotspot boards), then SET_CONFIG with DMR only, and nothing else (SET_CONFIG has no TX-off flag, so TX inhibit is the host's: with TX not allowed the driver never sends DMR_START or DMR data, see Regulatory): the modem starts idle and switches to DMR when the first DMR frame arrives.
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
- `dtoverlay=disable-bt` (current Raspberry Pi OS; older guides, MMDVM_HS's among them, call it `pi3-disable-bt`) to give the PL011 to the GPIO pins and turn Bluetooth off, or `dtoverlay=miniuart-bt` to keep Bluetooth on the mini UART. `rf-prepare` sets `disable-bt` and accepts either.
- `systemctl disable serial-getty@ttyAMA0.service` so no login prompt runs on the port, and `systemctl disable bluetooth.service` (and `hciuart` where present) when Bluetooth is off.
- Remove `console=serial0,115200` from `cmdline.txt` so the kernel console does not talk to the modem.
- A reboot is needed. The UI says so and offers it.
- Then `/dev/serial0` links to `/dev/ttyAMA0` (to verify on the current Raspberry Pi OS).

### Device access

- A udev rule in `packaging/udev/60-nodespot-node.rules` gives `ttyAMA0` group `nodespot-node`.
- `nodespot-node.service` gets `DeviceAllow=char-ttyAMA rw` (major name to verify; some kernels name the PL011 under `ttyS` or another class). Today it allows `char-alsa`, `char-hidraw` and `char-ttyUSB`. USB hats and boards (for example an MMDVM on a USB serial adapter) are already covered by `char-ttyUSB`, plus `char-ttyACM` for STM32 USB boards.

### GPIO reset

Many hats wire the STM32 boot and reset pins to Pi GPIO. The MMDVM_HS flash scripts use GPIO 20 for boot and GPIO 21 for reset on the MMDVM_HS hats and dual hat; other boards differ (for example 200 and 3 on one hotspot, 66 and 67 on another), so the pins are a per-board setting. A helper op pulses reset to recover a stuck modem. The node never touches GPIO itself.

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
- **As built:** `internal/native/radio` (`DigitalRadio`, `ModemStatus`), `internal/native/radio/mmdvm` (protocol, driver), `internal/native/radio/mmdvm/fake` (fake modem over an in-memory link or a pty), `internal/native/rf` (endpoint) and `mode.FrameMode` with `*dv.Mode` implementing it. Not wired into the config, UI, helper or packaging yet (no `mmdvm` preset). Routing details settled in M1: the hang is reserved per side (RF after an RF call, the network after a network call); an RF key-up takes a slot from a network call that is still in its prebuffer (3 frames) but not from one on the air; the loop guard applies after a call ends (while it is up the other side is busy anyway); TX timeout and frame timeout as above, with silence fill once the network falls behind the air by more than the prebuffer.

### M2: detect the hat, read version and status

- Helper ops `rf-prepare` and `rf-device-check`, the udev rule and `DeviceAllow`.
- GET_VERSION and GET_STATUS on the real hat; the board is identified from the description string and hardware info.
- The modem card in Settings > Station > Radio.
- **Test:** on the Pi 3B+: prepare, reboot, see the board name, firmware 1.6.1 and its protocol version, mode and flags in the UI. Record the serial stream as a fixture. This settles which hat it is and whether it speaks protocol 1 or 2.
- **As built:**
  - **Preset and config.** The radio preset `mmdvm` ("MMDVM hat (early preview)") has its settings in `station.radio.mmdvm` (`internal/native/node/mmdvm.go`): `device` (`/dev/serial0`), `baud` (115200; 38400 to 460800), `duplex` (on), `rxFreqHz`, `txFreqHz` (100 to 1000 MHz when set), `rxOffsetHz`, `txOffsetHz` (±100 kHz), `rfPower` (0 to 100), `colorCode` (1), `allowTx` (off) and `resetGpio` (optional, 0 to 1023). Any change to them, or to the preset, restarts the node daemon (the wiring-change path).
  - **Detection.** With the preset, `cmd/nodespot-node` (`modem.go`) builds the mmdvm driver with `DetectOnly` and an `rf.Endpoint` on it in place of the sound-chip path; the engine keeps the fake radio for audio. `DetectOnly` sends GET_VERSION (retrying until the hat answers) and then only GET_STATUS polls: no SET_FREQ, SET_CONFIG, SET_MODE or DMR frames, transmit stays inhibited whatever `allowTx` says, and Abort sends nothing. The driver parses the description (`ParseBoard`: name, version, build date and tag, TCXO, RF chip, dual, GitID; duplex capable when dual, or a non-hotspot board with no RF chip named) and the status (state name, enabled modes, flags, rising-edge RX and TX overflow counts). `Reopen` closes the port and opens it again at once.
  - **Fixtures.** `internal/native/radio/mmdvm/testdata/hs-dual-v1.6.1-version.hex` (the captured GET_VERSION, author callsigns scrubbed, the unique ID replaced with zeros) and `hs-dual-v1.6.1-status.hex` (the exact GET_STATUS, idle, all modes enabled, the fixed 10 for slot 1 before any duplex config). The fake modem answers with this version and protocol 1 by default. A replay test checks that a detect-only driver on them sends nothing but GET_VERSION and GET_STATUS.
  - **Node and API.** Control ops `modem_status` and `modem_reset`; `GET /api/radio/modem`, `POST /api/radio/modem/reset`, `GET /api/radio/device-check` and `POST /api/radio/prepare` (admin; see `docs/API.md`). The unique ID is never stored in the status, so neither the API nor the UI can show it.
  - **Helper** (`internal/helper/sys_rf.go`). `rf-prepare` refuses unless `/proc/device-tree/model` starts with "Raspberry Pi"; removes `console=serial0,…` (and `console=ttyAMA0,…`) from `/boot/firmware/cmdline.txt`; makes sure `enable_uart=1` and `dtoverlay=disable-bt` (or `pi3-disable-bt`) apply under `[all]` in `config.txt`, changing an `enable_uart=0` there in place and otherwise appending (after a new `[all]` line when the file ends in another section); writes only on change, atomically, keeping the first original as `<name>.nodespot-orig`; stops and disables `serial-getty@ttyAMA0` and `hciuart` when present and running or enabled (a missing unit, as `hciuart` on Debian 13, is skipped); and reports `rebootNeeded` with reasons (a boot file changed, the running kernel still has the serial console, or `/dev/serial0` does not point at `ttyAMA0` yet). `rf-device-check` reports the link target, whether it is a character device, owner, group, mode, whether the node's group can open it, the boot settings and problems. `bluetooth.service` is left alone: `disable-bt` already takes the radio off the UART.
  - **Packaging.** `60-nodespot-node.rules` gives `ttyAMA0` group `nodespot-node`, mode 0660 (with `:=`, since Raspberry Pi OS's `99-com.rules` sets `dialout` later); `nodespot-node.service` adds `DeviceAllow=char-ttyAMA rw`. `char-ttyACM` is not added yet.
  - **UI.** Settings > Station > Radio with the preset: the Channel card has the modem settings (frequencies in MHz, saved as Hz) and the modem card shows the state, board, firmware, protocol, modes, flags, overflow counts, reopens and the last error, polled every 3 s, plus the Pi's serial setup; admins get "Prepare the Pi" and "Reset modem".
  - **Backup.** The settings are in `node.json`; a restore sets `allowTx` to false.
  - **Not yet:** `rf-reset` (the GPIO pulse; `resetGpio` is stored only), a reboot button, RF receive (M3) and transmit (M4), the band table for frequencies, ESSID and slot settings.

### M3: DMR RX on one slot, in simplex (receive only)

The hat cannot hear a handheld in duplex until it transmits (see "Receive: duplex versus simplex"), so M3 sets the hat up in simplex and receives only. Duplex moves to M4, together with transmit.

- SET_FREQ (power 0), SET_CONFIG with the simplex bit and SET_MODE DMR, with TX off; RF RX on slot 2 to the active DMR network.
- **Test:** AT-D868UV on the hotspot channel (placeholder frequency, matching color code, slot 2), group call to a test talkgroup. The call reaches BrandMeister or TGIF (seen on the network's own last-heard page) and shows in nodespot's Last heard as N0CALL 1234567 via RF. Record RX fixtures.
- **As built:**
  - **Driver.** `mmdvm.Settings.RXOnly` makes the driver configure and listen but never transmit: it forces `AllowTX` off, `Modem.Duplex` off and `Freq.Power` 0, sends SET_FREQ (TX offsets and all), SET_CONFIG with the simplex bit and then SET_MODE DMR, and starts the status poll. `DetectOnly` (the M2 mode) clears `RXOnly`. `Driver.StartTX` returns `ErrTXInhibited`, and the serial writer drops any DMR_DATA1, DMR_DATA2, DMR_START or DMR_SHORTLC frame with a warning and counts it (`BlockedTX()`); tests assert the counter stays 0 and that the fake modem saw no such frame. A NAK of SET_FREQ (a frequency the board refuses) is reported with the band hint and the driver reopens the port as before.
  - **RX path.** `ModemStatus` gains `Receive` and `RXCall` (slot, source, destination, private, since). The driver decodes the burst stream into a call as before: header, voice, terminator, or DMR_LOST (the call ends as lost), and the AMBE frames go to the sink unchanged.
  - **Endpoint.** `rf.Config.ReceiveOnly` ignores a call from the network: the call is dropped and counted (`NetSkipped`), nothing is queued for the modem. An RF call with no mode linked is counted (`NoMode`) and logged.
  - **Engine.** `node.Options.RF` takes an `RFLink` (`Slots()` and `Link(slot, FrameMode)`), which `cmd/nodespot-node` fills with the endpoint. `Engine.syncRF` links slot 2 to the active mode when it is a `mode.FrameMode` that carries AMBE (BrandMeister, TGIF and DMR networks, `*dv.Mode`), and unlinks it whenever the active mode changes. With any other active mode (ASL, EchoLink, YSF, Parrot, M17, or none) RF calls are not passed on: the modem card says "RF calls are not passed to <mode>: ..." and the endpoint logs the call. Cross-mode bridging is M7.
  - **Talker.** `dv.Mode` raises a talker for an RF call (`Via: "rf"`, `Slot`, the radio ID, the target talkgroup), so Now calling, Last heard and the API show it with a "via RF" tag. The callsign is left empty and filled in from the DMR ID by the existing radioid lookup.
  - **Frequencies.** `ModemFreq()` applies the offsets (TX defaults to RX), and `CheckFreq()` runs when settings are saved (see "SET_FREQ rules").
  - **UI.** The modem card shows the receive state (configured, listening on slot 2, or receiving a call on slot 2 with source and destination) and the duplex note ("Duplex receive needs transmit, which comes in a later release. For now the modem listens in simplex on slot 2."), and the notes the node adds (no frequency set, a UHF-only board, RF not passed to the active mode). The settings form shows the frequency rules as live messages.
  - **Network checks and gaps (read from the code, not yet tried against a live network).**
    - BrandMeister (Rewind): `StartTX` takes the call's TG, private flag and source from the header, needs a logged-in session, and ignores the slot (always real-time 1; whether BrandMeister maps slot 2 to this is unverified).
    - Homebrew (TGIF and DMR networks): `StartTX` uses the network's own configured slot, not the RF slot.
    - `dv.Mode.SendHeader` sends the RF call's talkgroup whether or not the node is subscribed to it. On BrandMeister and TGIF the talkgroup should be a static or dynamic subscription of the account for return traffic, which M3 does not play to RF in any case.
    - Network-to-RF is off in M3 (`ReceiveOnly`).
    - A dual-chip hat set to simplex with a VHF frequency is only caught by the modem's own NAK.

  - **Calibrate.** `sudo nodespot-node calibrate` tunes the receive offset against a handheld; see "Calibrate (receive offset)" below.

## Calibrate (receive offset)

Hotspot crystals drift, so a handheld on the exact channel can sound clean on one hat and break up on another. `nodespot-node calibrate` measures how many bit errors the modem corrects while a handheld talks, and helps pick the `rxOffsetHz` that gives the fewest. It only receives: RF power stays 0, simplex is forced, and nothing is sent to the network.

```
sudo nodespot-node calibrate [-sweep] [-from -2000 -to 2000 -step 250] [-config path] [-dev path] [-yes]
sudo nodespot-node calibrate -rxfreq 433500000 [-txfreq Hz] [-offset Hz] [-cc 1] [-baud 115200] [-dev /dev/serial0] [-sweep ...]
```

- **Standalone form.** With `-rxfreq` (Hz, for example 433500000) no config is read, so it works on a Pi with no nodespot installed, only the hat on `/dev/serial0`. The modem is set up in simplex on that frequency (`-txfreq` defaults to it, and is checked with the same frequency rules), RF power 0, receive only, color code `-cc` (default 1), `-baud` (default 115200), starting at `-offset` Hz (default 0). It still needs root for the serial port. The service is stopped only if `nodespot-node.service` exists and is running; with no unit (or no systemctl) nothing is stopped or started. Saving is off: `s` and the end of a sweep print `standalone: set the RX offset to N Hz in Settings > Station > Radio` instead of writing a file, and a sweep never asks to save. `-txfreq`, `-offset`, `-cc` and `-baud` without `-rxfreq` are refused.

- **Setup.** The `mmdvm` preset with a receive frequency (for example 433.500 MHz), a handheld on the hotspot channel with the same color code, slot 2. It must run as root because it stops `nodespot-node.service` (the node owns the serial port) and rewrites the config. The service is started again on exit, on every path except SIGKILL, and only if the command stopped it. The modem is put back to idle before the service starts.
- **Manual mode** (default). Key up the handheld and watch the live BER (the last second), the call BER and the offset. Keys: `+` and `-` (or `=` and `_`) change the offset by 50 Hz, `]` and `[` by 500 Hz, `0` returns to the saved offset, `s` saves, `l` lists, `?` or `h` shows help, `q` or Ctrl-C quits. Each change retunes the modem at once.
- **Sweep mode** (`-sweep`). The command steps the offset from `-from` to `-to` in `-step` Hz, asks you to key up for about 3 seconds at each point, and prints a table of BER, bursts and lost fraction. It then suggests an offset and asks whether to save it. With `-yes` the result is saved without a prompt, but only when the minimum is not at an edge of the sweep and the curve is not flat; otherwise nothing is saved. Ctrl-C once ends the sweep early and still shows the table and the prompt; a second Ctrl-C, SIGTERM or SIGHUP stops at once.
- **Relative to the saved offset.** The sweep range is added to the offset already in the config (`rxOffsetHz`), so `-from -2000 -to 2000` covers 4 kHz around where you are. Points that the frequency check rejects (a banned range, a band edge) or that would put the offset beyond plus or minus 100000 Hz are skipped.
- **BER.** Corrected bit errors in the FEC-protected AMBE bits of each voice burst, divided by the bits checked: 141 per burst (3 frames of 24 + 23 protected bits). The 25 unprotected bits per frame are not counted. A frame whose first code word cannot be corrected counts as 10 errors, so the figure saturates and is a relative measure, not a true channel BER. A burst with 30 or more errors is unusable and a call that is mostly lost or unusable is not scored. Lost bursts are found from timestamp gaps (60 ms per burst). Calls shorter than the minimum are ignored.
- **Suggestion.** The lowest-BER point, then its contiguous neighbors within max(1 percentage point, 25% of the minimum); the suggestion is the middle of that run, rounded away from zero. It is flagged "edge" when the run touches the end of the sweep (widen the range) and "flat" when every point is good (any offset in the range works; nothing to tune).
- **Retune.** `mmdvm.Driver.Retune` sends SET_MODE idle, SET_FREQ (power 0, the new RX and the same TX offset) and SET_MODE DMR inside the driver's session, without closing the port. A NAK is reported and the driver stays up. Whether the idle and DMR bracket is needed on the real hat is not verified; it is the safe sequence.
- **Saving.** Only `rxOffsetHz` changes (and `txOffsetHz` is left as it was). The file is validated, written atomically and keeps its mode and owner, since the service runs as `nodespot-node` and the file is 0600.
- **Not included.** TX calibration and the firmware's own calibration modes (carrier, BER and RSSI tests) are not used. Transmit comes with M4.

### M4: DMR TX and duplex

- Allow transmit, TX path, timeouts, abort.
- Duplex: answer the radio's wake-up CSBK by starting TX (DMR_START 1), then receive on both slots while the hat transmits its downlink.
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

## Research list (still open)

- What a "1.6.1" modem speaks. Upstream MMDVM_HS ends at 1.6.0 and protocol 1; M2 settles it on the real hat.
- Raspberry Pi OS overlay names (`disable-bt` against `pi3-disable-bt`, `miniuart-bt`), the `serial0` link and the device class name for `DeviceAllow`.
- Boot and reset GPIO pins for the owner's hat and other common hats (only the MMDVM_HS flash scripts' pins are known).
- Whether Rewind can serve an RF hotspot well, or a Homebrew login to BrandMeister is better.
- Band table per region.

## Owner decisions

1. **Networks:** all of the DMR networks nodespot already has: BrandMeister, TGIF and DMR - Other (Homebrew masters, including DMR+). Each slot can be linked to any of them.
2. **Duplex:** the dual-slot hat runs duplex, with both slots in use. Because a duplex hat hears nothing from a handheld until it transmits, M3 receives in simplex and duplex arrives with transmit in M4.

Smaller design choices (a CW ID on RF, a separate DMR hang time) are noted where they arise and decided when the milestone is built.
