# aslcp 2.0 - Milestone 1 plan: native `nodespot-dv` engine skeleton, removal of the previous stack

Worktree: /home/user/wt-200 (branch try/2.0, from main 3832711). Everything below is one commit.

M1 does five things:
- It ships the `nodespot-dv` binary and unit, with a working USRP <-> md380 AMBE path and a loopback (parrot) network.
- It moves host-list downloading into the nodespot server.
- It deletes every line that drives the previous stack.
- It turns "Enable digital modes" into "install md380-emu, start nodespot-dv".
- It reserves the IMBE interface.

The DMR, YSF, P25 and NXDN protocols are out of scope. Their cards show "not yet available in this build".

Naming rule (minimal churn):
- New engine code lives in `internal/dv/...`.
- The panel-side Go package is renamed `internal/dvpanel`, the REST paths move to `/api/dv/...`, the SSE event is `dv`, the settings keys are `dv.*` (migrated once at store load), the favorites table is `dv_favorites`, and the web files are `Dv*.svelte` and `DigitalModes.svelte` at route `/digital`. The old names live only in `internal/legacy`. No aliases are kept (hard cut-over).
- Helper ops are rewritten anyway, so they get new `dv-*` names.

---

## A. Architecture

### A.1 Process model

```
browser ──HTTP/SSE──> nodespot (User=nodespot)
                        │  unix /run/nodespot/helper.sock (root helper: install, config render, systemctl)
                        │  unix /run/nodespot-dv/ctl.sock (NDJSON: status/tune/mode/unlink/reload/subscribe)
                        ▼
                     nodespot-dv (User=nodespot-dv, loopback only)
                        │ UDP 127.0.0.1:34001 (rx from Asterisk) / → 127.0.0.1:32001 (tx to Asterisk)
                        │ UDP → 127.0.0.1:2470 md380-emu (AMBE+2)
                        ▼
                     Asterisk chan_usrp (private bridge node, rxchannel=USRP/127.0.0.1:34001:32001)
```

Responsibilities:
- **nodespot-helper (root)** owns secrets and files outside /var/lib/nodespot. It renders `/var/lib/nodespot-dv/config.json` from the helper state dir `/var/lib/nodespot-dvstate/` (privatenode.json, networks.json). It runs the enable/disable jobs and restarts the units.
- **nodespot (server)** never reads the engine config. It sends runtime commands over ctl, subscribes to events, downloads the host lists and resolves list targets to host:port before a tune.
- **nodespot-dv** reads only its config file and talks only to loopback. It never reads host lists or files of the nodespot user.

### A.2 Goroutines inside nodespot-dv

| goroutine | owner | role |
|---|---|---|
| `usrp.Endpoint.readLoop` | usrp | Blocks on ReadFromUDP on 127.0.0.1:34001. It decodes packets and pushes `usrp.Packet` onto `rx chan` (cap 64). It drops packets whose source is not the configured peer IP. |
| `usrp.Endpoint` tx pacer | usrp | `time.Ticker` at 20 ms. It writes queued VOICE frames to 32001 and sends the keyup=0 header on end. The seq counter is per endpoint. The queue is capped at 50 frames (1 s); when full, the oldest frame is dropped. |
| `bridge.Router.Run` | bridge | The single owner of call state. It selects on USRP rx, network inbound, ctl commands and a 1 s housekeeping ticker. It emits events. |
| `vocoder.Worker` x1 per direction | vocoder | Encode worker (PCM to AMBE) and decode worker (AMBE to PCM). Each has its own UDP socket to md380-emu, so replies cannot be confused across directions. md380-emu is single-threaded; two sockets just queue there. |
| `network.Network.Run` | per network | M1 has only `loopback`. |
| `ctl.Server.Serve` + one goroutine per conn | ctl | Request/response, plus a per-subscriber writer with a 256-event buffer. A slow subscriber is disconnected with an `overflow` event rather than blocking the router. |
| signal handler | main | SIGTERM cancels the root ctx; the program sends keyup=0 if TX is active and exits within 2 s. SIGHUP equals `reload`. |

Router data flow:
- USRP VOICE (keyup=1): PCM goes to the encode worker, AMBE to `net.WriteVoice`.
- First keyup=1: `TalkStart{Dir:"tx"}`. keyup=0, or 300 ms without frames (timeout guard): `net.EndTX()` and `TalkEnd`.
- Network inbound voice: AMBE goes to the decode worker, PCM to the USRP tx pacer.
- Call header: `TalkStart{Dir:"rx"}`. Terminator, or 1.5 s silence: pacer end (keyup=0) and `TalkEnd`.
- A USRP TEXT (0x08 metadata) packet is sent to Asterisk at each rx call start: `TEXT` type with TLV tag 0x08, value `"<call> <tg>"` ASCII, at most 64 bytes. TX-side TEXT and DTMF from Asterisk are parsed and logged, but not acted on in M1. DTMF is reserved for M2 (dial-a-TG).

### A.3 md380 client and timeouts

- One request in flight per socket. Encode is 320 bytes out, 7 bytes in; decode is 7 bytes out, 320 bytes in. The reply length is checked.
- Read deadline 60 ms (config `vocoder.timeout_ms`, default 60).
- On a timeout the frame becomes silence (encode: a fixed AMBE silence frame `{0xb9,0xe8,0x81,0x52,0x61,0x73,0x00}`; decode: zero PCM) and `stats.Timeouts++`.
- After a timeout the socket is closed and re-dialled from a new ephemeral port. A late reply then lands on a closed port and can never be paired with the next request. Without this, the headerless protocol could hand back a reply that belongs to the previous request.
- After 25 consecutive timeouts, `vocoder_down` goes to status and an event `{type:"vocoder",ok:false}` is emitted. Retries continue with a 1 s backoff.

### A.4 Control socket

- **Path and lifetime.** `/run/nodespot-dv/ctl.sock`, created by nodespot-dv after it reads its config (RuntimeDirectory=nodespot-dv, RuntimeDirectoryMode=0750). There is no .socket unit, so a connect never starts a disabled engine.
- **Permissions.** Socket mode 0660, owner nodespot-dv:nodespot-dv. postinst creates the system user and group `nodespot-dv` and runs `adduser nodespot nodespot-dv`. nodespot-dv also checks SO_PEERCRED: the peer uid must be root or the uid of user `nodespot` (looked up at start; config `ctl.allow_uids` overrides it in tests). Anything else gets `{"ok":false,"code":"forbidden"}` and is closed.
- **Why not add nodespot-dv to group nodespot:** that would expose /var/lib/nodespot.
- **Framing.** UTF-8 NDJSON, one object per line, at most 64 KiB per line.
  - The client sends a `Request`; the server replies with a `Response` carrying the same `id`. Multiple requests per connection are allowed and processed in order.
  - `subscribe` turns the connection into an event stream: first a `Response` with the current `Status`, then `Event` lines. Further requests on that connection are rejected.
  - The idle timeout is 5 minutes for request connections and none for subscribers. The server sends `{"type":"ping"}` events every 15 s.
- **Server reaching it.** `ctl.Client` in nodespot is a dial-per-request client (2 s timeout) plus one long-lived subscriber goroutine. The subscriber reconnects with backoff 0.5 s doubling to 10 s and marks the snapshot `running:false` while disconnected. Behind `ProtectSystem=strict`, connecting to a socket in /run works (the helper socket is proof). nodespot.service needs no change; AF_UNIX is already allowed.

### A.5 Config model

`/var/lib/nodespot-dv/config.json` is owner nodespot-dv:nodespot-dv, mode 0600 (StateDirectoryMode=0700). Only the helper writes it: atomic temp file plus rename, then chown to the nodespot-dv uid/gid. The schema is `ctl.Config` (D.4).
- The USRP ports come from the private-node stanza the helper already reads (`rxchannel=USRP/127.0.0.1:34001:32001`, so listen 34001 and send to 32001).
- Identity and networks are copied from networks.json and privatenode.json. In M1 only `loopback` is enabled; the other networks are carried but marked `available:false`.
- nodespot-dv validates the file strictly (DisallowUnknownFields, version 1).
  - A bad file at start: exit 78 (EX_CONFIG), so systemd's RestartPreventExitStatus=78 stops the loop.
  - A bad file on `reload`: the old config is kept and `{"ok":false,"code":"bad_config"}` is returned.
- Without a file, the engine starts with the default USRP 34001/32001, md380 127.0.0.1:2470 and the loopback network, so a fresh enable works before the private node exists.

### A.6 systemd unit `packaging/systemd/nodespot-dv.service`

```ini
[Unit]
Description=nodespot digital voice engine
Documentation=file:///usr/share/doc/nodespot/README
Wants=md380-emu.service
After=network.target md380-emu.service asterisk.service

[Service]
Type=simple
User=nodespot-dv
Group=nodespot-dv
ExecStart=/usr/libexec/nodespot/nodespot-dv run
Restart=on-failure
RestartSec=2
RestartPreventExitStatus=78
StateDirectory=nodespot-dv
StateDirectoryMode=0700
RuntimeDirectory=nodespot-dv
RuntimeDirectoryMode=0750
UMask=0007
Nice=-5
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
# M1: loopback only. Relaxed (IPAddressDeny removed) when the first real network lands.
IPAddressDeny=any
IPAddressAllow=localhost

[Install]
WantedBy=multi-user.target
```

- A Nice of -5 needs no capability, because systemd applies it before dropping privileges.
- On enable, the helper also writes a drop-in `/etc/systemd/system/md380-emu.service.d/nodespot.conf` with `IPAddressDeny=any` and `IPAddressAllow=localhost`, because md380-emu binds 0.0.0.0:2470.
- The drop-in is removed by disable only if the helper's marker says nodespot installed md380-emu. Otherwise it stays; it is harmless.

### A.7 Binary `cmd/nodespot-dv`

Subcommands:
- `run`: the service. Flags: `-config` (default /var/lib/nodespot-dv/config.json) and `-ctl` (default /run/nodespot-dv/ctl.sock).
- `bench`: see A.8.
- `version`.

Logs go to stderr in plain text; the journal adds timestamps. Callsigns are logged only at debug level.

### A.8 `nodespot-dv bench`

`nodespot-dv bench [-md380 127.0.0.1:2470] [-frames 1500] [-mode encode|decode|roundtrip] [-concurrency 1]`

- It drives the real `vocoder.MD380` client with a synthetic 1 kHz sine (no real audio).
- It prints frames/s, the real-time factor (frames/s ÷ 50), p50/p90/p99/max latency, and timeouts.
- **Pass criterion:** roundtrip at concurrency 1 sustains at least 100 frames/s (2x real-time) and p99 is at most 15 ms. The exit code is non-zero otherwise.
- `-fake` runs against the in-process fake server, so CI can exercise the code path.
- Run the real bench on a Pi (armhf, native) and on arm64/amd64 (qemu-user-static). Record the results in docs/DV.md.

### A.9 Network interface (internal/dv/network)

See D.4. M1 implements only `loopback`, a parrot: it records the AMBE frames of one TX (up to 60 s), waits 1 s, then plays them back as an rx call from callsign `PARROT`, TG 9990. This proves both directions on a live node.

---

## B. File-by-file list

Paths are relative to /home/user/wt-200. Tags: [T1]/[T2]/[T3] = track owner.

### B.1 NEW

| file | reason |
|---|---|
| cmd/nodespot-dv/main.go [T1] | Subcommand dispatch (run, bench, version). |
| cmd/nodespot-dv/run.go [T1] | Wires config, usrp, vocoder, bridge and ctl; signals; exit 78 on bad config. |
| cmd/nodespot-dv/bench.go, bench_test.go [T1] | The bench (A.8); the test runs `-fake -frames 200`. |
| internal/dv/usrp/packet.go, packet_test.go [T1] | 32-byte header codec, types VOICE/DTMF/TEXT, TLV 0x08; table tests plus a fuzz test (FuzzParse). |
| internal/dv/usrp/endpoint.go, endpoint_test.go [T1] | UDP rx loop and 20 ms tx pacer, peer filter. |
| internal/dv/vocoder/vocoder.go [T1] | AMBE and IMBE interfaces, silence frames, Stats. |
| internal/dv/vocoder/md380.go, md380_test.go [T1] | UDP client with deadlines and re-dial (A.3). |
| internal/dv/vocoder/fake/fake.go [T1] | In-process fake md380 UDP server (deterministic codec, configurable delay/drop), used by tests and `bench -fake`. |
| internal/dv/network/network.go [T1] | Network interface and the frame/target/login types. |
| internal/dv/network/loopback/loopback.go, loopback_test.go [T1] | Parrot network. |
| internal/dv/bridge/router.go, router_test.go [T1] | Call state machine, timeouts, events. |
| internal/dv/bridge/loopback_audio_test.go [T1] | The end-to-end audio test (E.2). |
| internal/dv/ctl/types.go [T1, frozen first] | The shared contract (D.4). |
| internal/dv/ctl/server.go, server_test.go [T1] | NDJSON server, SO_PEERCRED check, subscriber fan-out. |
| internal/dv/ctl/client.go, client_test.go [T1] | Client used by nodespot (Do, Subscribe). |
| internal/dv/ctl/ctltest/fake.go [T1] | Fake engine on a temp socket, for track 3 tests. |
| internal/dv/config/config.go, config_test.go [T1] | Load/validate `ctl.Config` strictly; defaults. |
| internal/dv/imbe/* [T2] | IMBE port (separate agent); must satisfy `vocoder.IMBE`. |
| packaging/systemd/nodespot-dv.service [T1] | The unit in A.6. |
| internal/hostlists/hostlists.go, hostlists_test.go [T3] | Downloader and index (C.4), modelled on internal/radioid. |
| internal/hostlists/parse.go, parse_test.go [T3] | Parsers moved from helper/dv_lists.go (ParseDMRHosts, ParseYSFHosts, ParseTGHosts) plus the Pi-Star formats. |
| internal/hostlists/testdata/*.txt [T3] | Synthetic fixtures only (names like "Test Room", hosts 192.0.2.x / node2001.example.net). |
| internal/dvpanel/tune.go, tune_test.go [T3] | Validators moved out of helper: `ValidTune`, `UnlinkTune`, `NormMode`, `ParrotID`, `PrivateTune`, `DMRFamily`, `PanelModes`. |
| internal/dvpanel/info.go [T3] | Local `Info` struct (Found, Started, Updated, Mode, Tune, TG, Slot, Private) and `Caller` (replaces the old helper info type/DVCaller for the resume tracker and snapshot). |
| internal/helper/dv.go, dv_test.go [T3] | New ops: dv-status, dv-enable, dv-disable, dv-job, dv-config. Reuses keyring/source/marker code moved from dv.go. |
| internal/helper/dv_common.go [T3] | Kept symbols relocated out of the deleted files: `now`, `selfPath`, `osCodename`, `dvStateDir`, `dvBusy`, `dvNetMu`, `dvNetworksPath`, `encodeDVNetworks`, `parseDVNetworks`, `dvPrivatePath`, `dvPrivateMax`, `parseDVPrivate`, `DVPrivateRecord`, `DVUSRP`, `ReadDVPrivate`, `dvFiles`, `dvFilePath`, `dvOptDir`, `dvSaveHistory`, `dvUnitStates`, marker types, `DVPlatform`. |
| internal/helper/dv_config.go, dv_config_test.go [T3] | Renders `ctl.Config` from the state dir and the rpt.conf stanza; atomic write and chown. |
| internal/api/dv_engine.go, dv_engine_test.go [T3] | Server-side ctl subscriber; builds DVSnapshot and feeds nowcalling. |
| internal/api/handlers_hostlists.go [T3] | GET /api/dv/lists served from internal/hostlists. |
| docs/DV.md [T3] | Short operator/developer doc of the engine (replaces the previous stack guide). |

### B.2 DELETE (and what each caller becomes)

**internal/helper (all [T3]):**
- **Delete outright:** dv_bootmode.go, dv_cfg.go, dv_dmr.go, dv_idlookup.go, dv_tune.go, dv_stfu.go, dv_callers.go, dv_talks.go, dv_run.go, dv_networks.go (after moving the parse/encode functions into dv_common.go), dv_lists.go (parsers move to internal/hostlists), dv_step.go (replaced by a smaller `RunDVStep` in dv.go), dv_demo.go / NewDemothe previous stack (replaced by a demo `ctltest` fake in cmd/nodespot), and every `*_test.go` for those, plus stfu_test.go.
- **dv.go:** delete the file after moving the keyring, source, marker and platform code into dv.go and dv_common.go. dvInstallScript and dvUninstallScript are replaced by dvEnableScript.
- **dv_private.go:** delete DVUSRP, DVPrivate and the revert step. Keep the record type, `ReadDVPrivate`/`parseDVPrivate` (in dv_common.go) and `PlanPrivateRevert`, renamed `PlanPrivateRemove`, in dv.go. The private-node set/clear moves to a new op, `dv-private` (C.1).
- **Callers that break:**
  - **ops.go:** remove the DV fields (DVKeyring…dvIDs) except DVStateDir, SelfPath, HTTPGet and Now. Add `DVEngineDir` (default /var/lib/nodespot-dv) and `DVEngineUser` (default nodespot-dv). Replace the OpOldStack* switch cases with the five dv ops. Replace `isDVConfigOp` with `isDVOp` for the `req.DV` field check.
  - **proto.go:** remove the OpOldStack* constants and `the previous stack *DVRequest`. Add `DV *DVRequest \`json:"dv,omitempty"\`` (C.1).
  - **server.go:** update `knownOp` to the new list.
  - **client.go:** remove the OldStack* methods, `dvCall` and the dvErrors entries. Add `DVStatus`, `DVEnable`, `DVDisable`, `DVJob`, `DVConfig` and `DVPrivate`, with error codes not_supported, busy, not_enabled and bad_request.
  - **sys_logs.go:** the `dvUnits` list becomes `[]string{"nodespot-dv","md380-emu"}`, plus the enable job unit.
  - **sys_diag.go:** the config/dv/ collection collects /var/lib/nodespot-dv/config.json with secrets masked (passwords blanked through the same mask helper), and the /opt ini files only if present (read-only, for M6 diagnosis).
  - **osupdate.go `aptJobRunning` and parse.go `dvJobUnit`:** the unit names become `nodespot-dv-enable`.
  - **backup_local.go:** remove `writeDemoDVLists` and the DVOptDir/DVHostDir/DVLogDir/DVHistoryDir seeding. Keep the demo privatenode.json/networks.json seeding.
  - **backup_dv_pending.go:** it called DVStatus/DVRestoreDiscard. It becomes a pending-status read from the state dir; restore-discard stays as part of the `dv-status` reply (`restorePending`), and the discard op is kept as `dv-restore-discard`.
  - **cmd/nodespot-helper/main.go:** the old step argv becomes `dv-step`; no alias is kept.

**internal/api [T3]:**
- **handlers_dv.go:** rewrite (MODIFY below). Delete the handlers for job, install, uninstall, dmr-status, setup, identity, credentials, modes, brandmeister, brandmeister/enabled, networks/{id}, networks/{id}/enabled, stfu/install, restart and rollback, and their tests.
- **dv_snapshot.go:** rewrite as dv_engine.go. The old file and its test are deleted.

**cmd/nodespot [T3]:** the old command files become cmd/nodespot/dv.go and dv_test.go: `nodespot dv enable|disable|status`; the old install command is not kept as an alias.

**web [T3]:**
- **Delete:** DvBM.svelte, lib/dvbm(.test).ts, lib/dmrlogin(.test).ts, lib/dvjob(.test).ts and the Identity, Credentials, TGIF, DMR-Other, Services-restart and Rollback sections of DvSetup.svelte.
- **api.ts:** remove dvJob, Install, Uninstall, Setup, Identity, Credentials, Modes, BM*, Network*, STFUInstall, Restart and Rollback; add dvEnable and dvDisable.
- **mock/dv.ts:** trim to the kept shapes.
- **package.json "test":** drop the deleted test files.

**docs:** the previous stack guide is deleted and replaced by docs/DV.md.

### B.3 KEEP unchanged

- internal/legacy/ini/* (no importers after M1; kept with ini_test.go for the M6 import).
- internal/dvpanel/linkrec.go and its test.
- The favorites store code, configsvc/mask.go, susb/files.go `USRPNode`, api/public_bridge.go, internal/public/*, internal/radioid/*, nowcalling package internals.
- internal/backup/backup.go: the the previous stack part, file constants and manifest stay, so old backups still restore; the ini files keep staging into pending for M6.

### B.4 MODIFY

| file | change |
|---|---|
| internal/dvpanel/resume.go, resume_test.go [T3] | `Source` becomes `Info(ctx) (dv.Info, error)` and `Tune(ctx, mode, tune string) (bool, error)`. Validators come from tune.go. The `BootMode` hook is dropped and `ModeEnabled` kept. Test values: TG 91 is replaced by placeholder TGs such as 1997/1998. |
| internal/api/handlers_dv.go, tests [T3] | The previous interface becomes `DVEngine` (C.2 shows the routes kept). |
| internal/api/handlers_dv_ctl.go, tests [T3] | mode, unlink and tune go through ctl. The `dvCmdReply` drops BMLoginPending, STFUStopFailed, NetRestarted and NetLoginPending. Favorites are unchanged. |
| internal/api/handlers_dv_private.go, tests [T3] | Steps: "ini" becomes "engine_config" (helper dv-config) and "bridge_restart" becomes "engine_reload" (ctl reload; falls back to helper `dv-restart` when ctl is unreachable). The record set/clear goes through helper op dv-private. |
| internal/api/nowcalling.go [T3] | `ncDV(info the old helper info type, ...)` becomes `ncDV(talks []nowcalling.Talk, sn DVSnapshot)`, fed from TalkEvents. Via becomes "nodespot-dv". `ncTGName` uses `dv.DMRFamily`. `ncKick` becomes a no-op, because events arrive in real time; remove it and its call. |
| internal/api/server.go [T3] | The previous dependency becomes `DVEngine DVEngine` (the helper part), plus `DVCtl *ctl.Client` and `HostLists *hostlists.Manager`. `DVInstalled` becomes `DVEnabled func() bool`. Keep DVResume, DVBridgeNode, RadioID and DVLink. `initthe previous stack` becomes `initDVEngine`. |
| cmd/nodespot/main.go [T3] | Wire ctl.Client (/run/nodespot-dv/ctl.sock), hostlists.New(dataDir+"/hostlists", ...), and the resume tracker with an adapter over the ctl client. `dvInstalled` (stat of /opt/Analog_Bridge) becomes a helper `dv-status` cached `enabled`. Demo mode runs `ctltest.Fake` in-process. |
| internal/nowcalling/nowcalling.go [T3] | Add the `ViaEngine = "nodespot-dv"` constant; keep the old Via values for snapshot compatibility. |
| internal/helper/backup_dv*.go [T3] | Use the relocated symbols. Remove restore-apply from any job. The pending set stays for M6. |
| packaging/nfpm.yaml [T1] | Add `/usr/libexec/nodespot/nodespot-dv` (mode 0755) and `/usr/lib/systemd/system/nodespot-dv.service`; add `qemu-user-static` nowhere (it is installed at enable time). |
| Makefile [T1] | deb-pkg loop `for c in nodespot nodespot-helper nodespot-dv`; build/build-arm64/build-armhf also build nodespot-dv (CGO_ENABLED=0, GOARM=7). |
| packaging/scripts/postinst.sh [T1] | `adduser --system --group --no-create-home --home /var/lib/nodespot-dv nodespot-dv`, then `adduser nodespot nodespot-dv`. If nodespot-dv.service is enabled, restart it after daemon-reload. Do not enable it. |
| packaging/scripts/prerm.sh [T1] | On remove, stop and disable nodespot-dv.service. |
| packaging/scripts/postrm.sh [T1] | On purge, remove /var/lib/nodespot-dv and `deluser --system nodespot-dv`. Keep the existing /var/lib/nodespot-dvstate purge. |
| packaging/install.sh [T3] | `NODESPOT_DV=1` runs `nodespot dv enable`; the old variable is dropped. |
| packaging/README.package [T1] | Mention nodespot-dv and its state dir. |
| .github/workflows/ci.yml [T1] | Package-contents check for nodespot-dv; add `npm test` in web; add `go run ./cmd/nodespot-dv bench -fake -frames 200`. |
| web/src/components/DigitalModes.svelte [T3] | Becomes "Digital modes": an Enable/Disable button, job log (via dv-job), status of nodespot-dv/md380-emu, restore-pending notice. No uninstall. |
| web/src/components/DvSetup.svelte [T3] | Keep Resume, the per-mode cards, DvBridge (private node) and favorites. A card whose mode has `available:false` shows "Not yet available in this build" and disabled controls; loopback shows as "Parrot (test)". |
| web/src/components/DvCard.svelte, lib/dvpanel, dvmodes, dvlist, dvmsg, dvnetworks, dvbridge (+tests) [T3] | Adapt to the snapshot changes (C.3); dvnetworks stays read-only. |
| web/src/lib/store.svelte.ts [T3] | The `dv` SSE stays; the type gains the new fields. |
| docs/API.md, docs/DESIGN.md, docs/BACKUP.md [T3] | Update the endpoints, process model and backup notes. |
| CHANGELOG.md [T3] | Operator-visible items only (E.4). |

---

## C. Helper ops, API endpoints and UI

### C.1 Helper ops (proto.go; `knownOp`)

```go
const (
	OpDVStatus         = "dv-status"          // -> DVStatus
	OpDVEnable         = "dv-enable"          // starts job unit nodespot-dv-enable
	OpDVDisable        = "dv-disable"         // systemctl disable --now nodespot-dv
	OpDVJob            = "dv-job"             // -> DVJob (state + last 200 journal lines of the job unit)
	OpDVConfig         = "dv-config"          // render /var/lib/nodespot-dv/config.json
	OpDVPrivate        = "dv-private"         // set/clear privatenode.json record (was part of dv-private)
	OpDVRestart        = "dv-restart"         // systemctl restart nodespot-dv (fallback when ctl is unreachable)
	OpDVRestoreDiscard = "dv-restore-discard" // kept from dv-restore-discard
)

type DVRequest struct {
	Private *DVPrivateRecord `json:"private,omitempty"` // dv-private set
	Clear   bool             `json:"clear,omitempty"`   // dv-private clear
}

type DVStatus struct {
	Supported      bool   `json:"supported"`      // DVPlatform ok (bookworm/trixie; amd64/arm64/armhf)
	Reason         string `json:"reason,omitempty"`
	MD380Installed bool   `json:"md380Installed"`
	Enabled        bool   `json:"enabled"` // nodespot-dv.service is-enabled
	Active         bool   `json:"active"`
	MD380Active    bool   `json:"md380Active"`
	Job            string `json:"job,omitempty"` // "", running, failed, done
	LegacyUnits    []string `json:"legacyUnits,omitempty"` // the previous stack units still loaded
	RestorePending bool   `json:"restorePending"`
}
```

The enable job is a transient unit. The script `dvEnableScript`, as root, runs these steps:
1. Write the pinned keyring and the apt source, using the existing code and fingerprint.
2. `apt-get update -o Dir::Etc::sourcelist=<dv list>`.
3. Simulate `apt-get install md380-emu [qemu-user-static if arch != armhf]`, then `nodespot-helper dv-step install-plan`. The plan step keeps the existing unexpected_deps guard and the marker.
4. Install.
5. For each legacy unit of analog_bridge, mmdvm_bridge, netcheck, ysfgateway, p25gateway, nxdngateway and stfu that is loaded: `systemctl disable --now`. This frees port 34001; nothing is purged.
6. Write the md380-emu drop-in.
7. `nodespot-helper dv-step config`.
8. `systemctl daemon-reload` and `systemctl enable --now md380-emu nodespot-dv`.
9. `nodespot-helper dv-step check`. It waits up to 10 s for /run/nodespot-dv/ctl.sock and a `status` with `vocoder.ok`, and prints a warning otherwise.

Steps of `RunDVStep`: install-plan, config, check. ARMv6 (detected through /proc/cpuinfo or `dpkg --print-architecture` = armhf plus no `v7l`/`v8`) returns `Supported:false, Reason:"armv6_unsupported"`.

`dv-disable` runs `systemctl disable --now nodespot-dv` and keeps md380-emu and the marker.

### C.2 API endpoints (all under /api/dv, roles unchanged)

**Kept:**
- GET `` (DVSnapshot)
- GET /snapshot
- POST /mode
- POST /unlink
- POST /tune
- GET and PUT /resume
- GET /lists?mode=
- favorites CRUD
- private-node PUT/DELETE, adopt, rollback, name
- PUT /link
- the userlist routes
- DELETE /restore-pending

**New:**
- POST /enable (admin), which returns 202 with DVStatus.
- POST /disable (admin), which returns 200.
- GET /status (admin), which returns DVStatus plus `job` lines.

GET /job is kept as the job log for the enable unit, so the UI's job poller logic survives with a new unit name.

**Removed** (404 after M1): install, uninstall, setup, identity, credentials, modes, brandmeister(+/enabled), networks/{id}(+/enabled), stfu/install, dmr-status, restart and rollback. Document them in API.md under "Removed in 2.0".

**POST /tune flow:**
1. Validate with `dv.ValidTune`.
2. For YSF/P25/NXDN, resolve the target with `HostLists.Resolve(mode, value)`, giving a `ctl.Target{Host,Port,Name}`; an unknown value returns 422 `unknown_target`.
3. Send `ctl.TuneArgs`.
4. A mode with `available:false` returns 409 `not_available` with the text "not yet available in this build".

### C.3 Snapshot changes (DVSnapshot)

- `Installed` means md380-emu is installed. `Running` means ctl is connected. `Configured` means config.json exists.
- `Modes[i]` gains `Available bool`: M1 marks only `LOOP` true; BM, TGIF, DMR, YSF, P25 and NXDN are false.
- `Services` becomes `[{nodespot-dv},{md380-emu}]`.
- `Bridge.Source` becomes "nodespot-dv".
- `Caller` and `Heard` come from TalkEvents (a ring of 10) and use the local `dv.Caller`. Its JSON is identical to helper.DVCaller, so the web is unaffected.
- New fields: `Vocoder {ok bool, timeouts uint64}` and `Engine {version string, uptime int}`.

### C.4 Host lists (internal/hostlists, server-side)

- **Files.** `/var/lib/nodespot/hostlists/{DMR_Hosts.txt,YSF_Hosts.txt,P25_Hosts.txt,NXDN_Hosts.txt}` plus `meta.json` (ETag, Last-Modified, fetched time, rows). The nodespot user owns them; this is inside ReadWritePaths.
- **Source.** `https://www.pistar.uk/downloads/<name>`; the base URL can be overridden by the setting `hostlists.base_url` (tests and mirrors).
- **Who downloads.** The nodespot server: `hostlists.Manager.Run(ctx)`, started from main like radioid.
- **When.**
  - First check 60 s after start, then every 24 h with ±1 h jitter.
  - Conditional GET with If-None-Match/If-Modified-Since.
  - Only while digital modes are enabled (`DVEnabled()`).
  - A manual POST /api/dv/lists/refresh (admin) is limited to once per 10 minutes.
- **Limits.**
  - At most 4 MiB per file and 30 s timeout.
  - The file is written to a temp file, parsed, and must give more than 0 rows before it is renamed into place; the last good copy is kept.
  - 5000 items per list, as today.
- **User-Agent:** `nodespot/<version>`, generic, with no callsign, node or ID.
- **API:** `Get(mode) (DVList, error)` (same JSON shape as today, so dvlist.ts is unchanged), `Resolve(mode, value) (Target, bool)` and `Status()`.
- **DMRIds.dat is not downloaded.** internal/radioid's user.csv already supplies ID→callsign plus name and location (a superset), checked daily, with ETag. A second list would duplicate data and bandwidth. If the engine ever needs IDs offline, add DMRIds.dat as one more entry in the same manager. Flag this to the owner as a decision.
- **Format check.** The Pi-Star file names differ from the previous stack's (YSF_Hosts vs YSFHosts). Before writing the parsers, verify the column layouts against a downloaded copy. Commit only synthetic fixtures.

### C.5 UI

- **Header nav:** "the previous stack" becomes "Digital modes", still at route `/dv`.
- **DigitalModes.svelte:** enable/disable, job log, status rows for both units, the legacy-units notice ("the previous stack services were stopped; they will be removed in a later version"), the ARMv6 notice and restore-pending.
- **DvSetup.svelte:** Resume, Bridge (private node), the mode cards (unavailable ones gray with "Not yet available in this build"), favorites, and a Parrot card with a "Tune parrot" button.
- **Dashboard DvCard:** unchanged layout; it shows the parrot as the mode and the callers from events.
- **UserListCard:** unchanged.

---

## D. Parallel tracks

**Ownership** (disjoint):
- **T1 (engine + packaging):** cmd/nodespot-dv/**, internal/dv/** except internal/dv/imbe, packaging/systemd/nodespot-dv.service, packaging/nfpm.yaml, packaging/scripts/{postinst,prerm,postrm}.sh, packaging/README.package, Makefile, .github/workflows/ci.yml.
- **T2 (IMBE):** internal/dv/imbe/** only.
- **T3 (rewiring):** everything else listed in B: internal/helper/**, internal/api/**, internal/dvpanel/** (except ini, untouched), internal/hostlists/**, internal/nowcalling, cmd/nodespot/**, cmd/nodespot-helper/main.go, packaging/install.sh, web/**, docs/**, CHANGELOG.md.

**Sequencing:**
1. T1 commits `internal/dv/ctl/types.go` and `ctltest` first, as hour-zero work. T3 builds against them.
2. T2 is independent; it needs only the `vocoder.IMBE` signature below, which it implements structurally without importing vocoder.
3. Merge order: T1 types, then T1 and T2 in parallel, then T3. Final `go test -race ./...` on the merged tree.
4. A contract change after the freeze requires agreement between T1 and T3.

### D.4 Shared contract (internal/dv/ctl/types.go, frozen)

```go
// Package ctl is the control contract between the nodespot server and nodespot-dv:
// newline-delimited JSON over the unix socket /run/nodespot-dv/ctl.sock.
package ctl

import (
	"encoding/json"
	"time"
)

const (
	SocketPath    = "/run/nodespot-dv/ctl.sock"
	ConfigPath    = "/var/lib/nodespot-dv/config.json"
	ConfigVersion = 1
	MaxLine       = 64 << 10
)

// Ops.
const (
	OpPing      = "ping"
	OpStatus    = "status"
	OpReload    = "reload"    // re-read ConfigPath (the "apply config" request)
	OpMode      = "mode"      // ModeArgs
	OpTune      = "tune"      // TuneArgs
	OpUnlink    = "unlink"    // UnlinkArgs
	OpSubscribe = "subscribe" // connection becomes an event stream
)

// Error codes.
const (
	CodeBadRequest   = "bad_request"
	CodeForbidden    = "forbidden"
	CodeBadConfig    = "bad_config"
	CodeNotAvailable = "not_available" // network not implemented in this build
	CodeNotEnabled   = "not_enabled"   // network disabled in config
	CodeBusy         = "busy"          // a call is in progress and the op would cut it
	CodeInternal     = "internal"
)

type Request struct {
	ID   uint64          `json:"id"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

type Response struct {
	ID     uint64          `json:"id"`
	OK     bool            `json:"ok"`
	Code   string          `json:"code,omitempty"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"` // Status for status/subscribe/mode/tune/unlink/reload
}

// Mode ids. M1 implements ModeLoop only.
const (
	ModeNone = ""
	ModeLoop = "LOOP"
	ModeBM   = "BM"
	ModeTGIF = "TGIF"
	ModeDMR  = "DMR"
	ModeYSF  = "YSF"
	ModeP25  = "P25"
	ModeNXDN = "NXDN"
)

type ModeArgs struct {
	Mode  string `json:"mode"`
	Force bool   `json:"force,omitempty"` // cut an active call
}

// Target is where a tune points. For DMR-family modes TG (and Private) are
// used; for YSF/P25/NXDN the server resolves the host list entry first.
type Target struct {
	TG      uint32 `json:"tg,omitempty"`
	Slot    int    `json:"slot,omitempty"` // 1 or 2; 0 = network default
	Private bool   `json:"private,omitempty"`
	Room    string `json:"room,omitempty"` // YSF reflector id / P25,NXDN TG as text
	Name    string `json:"name,omitempty"`
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
}

type TuneArgs struct {
	Mode   string `json:"mode"`
	Target Target `json:"target"`
	Force  bool   `json:"force,omitempty"`
}

type UnlinkArgs struct {
	Mode string `json:"mode,omitempty"` // "" = current
}

type LoginState string

const (
	LoginOff     LoginState = "off"
	LoginPending LoginState = "pending"
	LoginOK      LoginState = "ok"
	LoginFailed  LoginState = "failed"
)

type NetStatus struct {
	Mode      string     `json:"mode"`
	Available bool       `json:"available"` // implemented in this build
	Enabled   bool       `json:"enabled"`   // enabled in config
	Login     LoginState `json:"login"`
	Detail    string     `json:"detail,omitempty"`
}

type VocoderStatus struct {
	OK       bool   `json:"ok"`
	Addr     string `json:"addr"`
	Frames   uint64 `json:"frames"`
	Timeouts uint64 `json:"timeouts"`
}

type Status struct {
	Version  string        `json:"version"`
	Started  time.Time     `json:"started"`
	Mode     string        `json:"mode"`
	Target   *Target       `json:"target,omitempty"`
	Updated  time.Time     `json:"updated"`
	USRP     USRPStatus    `json:"usrp"`
	Vocoder  VocoderStatus `json:"vocoder"`
	Networks []NetStatus   `json:"networks"`
	Talk     *Talk         `json:"talk,omitempty"` // call in progress
}

type USRPStatus struct {
	Listen   string    `json:"listen"` // 127.0.0.1:34001
	Peer     string    `json:"peer"`   // 127.0.0.1:32001
	LastRx   time.Time `json:"lastRx,omitempty"`
	RxFrames uint64    `json:"rxFrames"`
	TxFrames uint64    `json:"txFrames"`
}

// Talk is one call. Dir rx = from the network to the node, tx = from the node.
type Talk struct {
	ID       uint64        `json:"id"`
	Dir      string        `json:"dir"` // "rx" | "tx"
	Mode     string        `json:"mode"`
	Call     string        `json:"call,omitempty"`
	DMRID    uint32        `json:"dmrId,omitempty"`
	TG       uint32        `json:"tg,omitempty"`
	Room     string        `json:"room,omitempty"`
	Slot     int           `json:"slot,omitempty"`
	Private  bool          `json:"private,omitempty"`
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end,omitempty"`
	Duration time.Duration `json:"durationNs,omitempty"`
}

// Event types.
const (
	EvStatus    = "status"     // Status changed (mode/target/network)
	EvTalkStart = "talk_start" // Talk
	EvTalkEnd   = "talk_end"   // Talk with End and Duration
	EvLogin     = "login"      // Login
	EvVocoder   = "vocoder"    // VocoderStatus
	EvPing      = "ping"
	EvOverflow  = "overflow" // subscriber too slow; server closes
)

type Event struct {
	Type    string         `json:"type"`
	At      time.Time      `json:"at"`
	Status  *Status        `json:"status,omitempty"`
	Talk    *Talk          `json:"talk,omitempty"`
	Login   *LoginEvent    `json:"login,omitempty"`
	Vocoder *VocoderStatus `json:"vocoder,omitempty"`
}

type LoginEvent struct {
	Mode   string     `json:"mode"`
	State  LoginState `json:"state"`
	Detail string     `json:"detail,omitempty"`
}

// Config is ConfigPath, rendered by nodespot-helper (dv-config), mode 0600 nodespot-dv.
type Config struct {
	Version  int            `json:"version"`
	USRP     USRPConfig     `json:"usrp"`
	Vocoder  VocoderConfig  `json:"vocoder"`
	Identity Identity       `json:"identity"`
	Networks []NetConfig    `json:"networks"`
	Ctl      CtlConfig      `json:"ctl"`
	Resume   *TuneArgs      `json:"resume,omitempty"` // tune at start (written by the server via helper in M2; ignored if nil)
}

type USRPConfig struct {
	Listen string `json:"listen"` // "127.0.0.1:34001"
	Peer   string `json:"peer"`   // "127.0.0.1:32001"
	GainRx int    `json:"gainRxDb,omitempty"` // -20..20
	GainTx int    `json:"gainTxDb,omitempty"`
}

type VocoderConfig struct {
	MD380     string `json:"md380"`     // "127.0.0.1:2470"
	TimeoutMS int    `json:"timeoutMs"` // default 60
}

type Identity struct {
	Callsign   string `json:"callsign,omitempty"` // tests: N0CALL
	DMRID      uint32 `json:"dmrId,omitempty"`    // tests: 1234567
	RepeaterID uint32 `json:"repeaterId,omitempty"`
}

// NetConfig carries secrets: never logged, never in Status.
type NetConfig struct {
	Mode     string `json:"mode"`
	Enabled  bool   `json:"enabled"`
	Address  string `json:"address,omitempty"`
	Port     int    `json:"port,omitempty"`
	Password string `json:"password,omitempty"`
	Options  map[string]string `json:"options,omitempty"`
}

type CtlConfig struct {
	Socket    string   `json:"socket,omitempty"`
	AllowUIDs []int    `json:"allowUids,omitempty"` // tests only; default root + user nodespot
}
```

`ctl.Client` API (T1 implements it; T3 consumes it):

```go
func NewClient(path string) *Client
func (c *Client) Status(ctx context.Context) (Status, error)
func (c *Client) Reload(ctx context.Context) (Status, error)
func (c *Client) Mode(ctx context.Context, a ModeArgs) (Status, error)
func (c *Client) Tune(ctx context.Context, a TuneArgs) (Status, error)
func (c *Client) Unlink(ctx context.Context, a UnlinkArgs) (Status, error)
// Subscribe calls fn with the initial Status, then each Event, until ctx ends
// or the connection drops (returns the error; caller reconnects).
func (c *Client) Subscribe(ctx context.Context, first func(Status), fn func(Event)) error

type Error struct{ Code, Msg string } // returned for ok:false
```

`ctltest.Fake`: `New(t) *Fake` (temp socket path, AllowUIDs = self), `Path()`, `SetStatus(Status)`, `Emit(Event)`, and `Requests() []Request`.

### D.5 Engine-internal interfaces (T1; T2 relies on vocoder.IMBE)

```go
package vocoder

const (
	PCMSamples = 160 // 20 ms at 8 kHz
	AMBEBytes  = 7   // AMBE+2 49 bits
	IMBEBytes  = 11  // IMBE 88 bits
)

type AMBE interface {
	Encode(ctx context.Context, pcm *[PCMSamples]int16) ([AMBEBytes]byte, error)
	Decode(ctx context.Context, f [AMBEBytes]byte) ([PCMSamples]int16, error)
}

// IMBE is implemented by internal/dv/imbe (pure Go, stateful, not safe for
// concurrent use: one instance per direction and call).
// Bit packing: u0..u7 vectors (12,12,12,12,11,11,11,7 bits) MSB-first.
type IMBE interface {
	Encode(pcm *[PCMSamples]int16) [IMBEBytes]byte
	Decode(f *[IMBEBytes]byte) [PCMSamples]int16
}
```

T2 contract: `package imbe` exports `func NewEncoder() *Encoder`, `func NewDecoder() *Decoder`, and a `Codec` struct combining both that satisfies `vocoder.IMBE`. It has no dependencies outside the standard library and CGO_ENABLED=0. The compile-time assertion `var _ vocoder.IMBE = (*imbe.Codec)(nil)` lives in a T1 test file (internal/dv/vocoder/imbe_assert_test.go), added after T2 merges, so T2 never edits outside its directory.

```go
package network

type Codec int
const ( CodecAMBE Codec = iota; CodecIMBE )

type VoiceFrame struct {
	Call  uint64 // ctl.Talk.ID
	Seq   uint32
	Data  []byte // 7 (AMBE) or 11 (IMBE) bytes
}

type CallHeader struct {
	Call    uint64
	Src     string // callsign
	SrcID   uint32
	TG      uint32
	Room    string
	Slot    int
	Private bool
}

// Sink receives what a network hears. Calls come from the network's goroutine;
// implementations (the bridge router) must not block.
type Sink interface {
	Header(h CallHeader)
	Voice(f VoiceFrame)
	End(call uint64)
	Login(ctl.LoginState, string)
}

type Network interface {
	Mode() string // ctl.Mode*
	Codec() Codec
	Run(ctx context.Context, s Sink) error // connect/login/keepalive until ctx ends
	Tune(ctx context.Context, t ctl.Target) error
	Unlink(ctx context.Context) error
	// Local TX from the node.
	StartTX(h CallHeader) error
	WriteVoice(f VoiceFrame) error
	EndTX(call uint64) error
	Status() ctl.NetStatus
}

// Factory builds a network from its config; M1 registers only "LOOP".
type Factory func(cfg ctl.NetConfig, id ctl.Identity) (Network, error)
func Register(mode string, f Factory)
func New(cfg ctl.NetConfig, id ctl.Identity) (Network, error) // CodeNotAvailable for unregistered modes
```

```go
package usrp

const HeaderLen = 32
type Type uint32
const ( TypeVoice Type = 0; TypeDTMF Type = 1; TypeText Type = 2 )
const TLVSetInfo = 0x08

// Header fields: eye "USRP" [0:4], Seq BE [4:8], Memory [8:12], Keyup BE [12:16],
// Talkgroup [16:20], Type BE [20:24], MpxID [24:28], Reserved [28:32].
type Packet struct {
	Seq       uint32
	Memory    uint32
	Keyup     bool
	Talkgroup uint32
	Type      Type
	PCM       *[160]int16 // VOICE: 320 bytes little-endian s16
	DTMF      byte
	Text      []byte      // TEXT: raw TLV
}
func Parse(b []byte) (Packet, error)
func (p *Packet) Append(dst []byte) []byte
func MetaTLV(s string) []byte // tag 0x08, len, ASCII (<= 64)
```

---

## E. Risks and verification

### E.1 Risks and mitigations

1. **Port 34001 is held by Analog_Bridge** on upgraded nodes. The enable job stops the legacy units. nodespot-dv retries the bind every 2 s and reports `usrp.listen` errors in status. The UI shows the legacy-units notice.
2. **The headerless md380 protocol mismatches replies after a timeout.** Mitigated by the per-direction socket and re-dial on timeout (A.3); covered by a test with fake delay > timeout.
3. **qemu-user-static speed on arm64/amd64.** The bench gate (2x real-time, p99 at most 15 ms) is run on real hardware before release; results go in DV.md. The fallback (out of M1) is native IMBE only for P25.
4. **md380-emu binds 0.0.0.0.** The drop-in IP restriction is written by enable.
5. **Secrets leakage.** Config 0600 nodespot-dv, `Status` never carries NetConfig, diagnostics mask passwords, and the server never reads the file.
6. **Socket permissions after upgrade.** The nodespot process gains group nodespot-dv only after restart; postinst restarts nodespot.service after `adduser`. SO_PEERCRED is the real gate anyway.
7. **Hard cut-over loses features** (BM/TGIF/DMR/YSF/P25/NXDN until later milestones). This is clearly stated in the CHANGELOG and the UI; settings (networks.json, favorites, resume) are kept so later milestones pick them up.
8. **Breakage breadth in T3** (helper symbols used by kept files). Do the B.2 relocation list before deleting files; `go build ./...` after each file group.
9. **Pi-Star list formats/names** differ from what the old parsers expected. Verify them against a live download before finalising the parsers; there are fixture tests per format.
10. **Backups from 1.x restore the previous stack ini files that are no longer used.** They stage into pending (unchanged) for M6; the UI notice explains this.
11. **The private-node flow loses its "bridge_restart" semantics.** Rollback state still lives in memory (Server.dvUndo); the new step names must be mirrored in web/lib/dvbridge.ts step labels.

### E.2 Loopback audio test (internal/dv/bridge/loopback_audio_test.go)

1. Start `fake.Server` on 127.0.0.1:0. Its deterministic codec:
   - encode: AMBE = `{0xA5, byte(s0>>8), byte(s0), 0,0,0, seqByte}`, where s0 is the first sample;
   - decode: 160 samples = int16 of bytes 1-2.
2. Start a fake chan_usrp peer: a UDP socket on 127.0.0.1:0 as Peer, with the engine's Listen on 127.0.0.1:0.
3. Build a Router with the loopback network (playback delay overridden to 10 ms).
4. The peer sends 50 VOICE frames (keyup=1), where frame k has all samples = 1000+k, then a bare keyup=0 header.
5. Expect, within 3 s:
   - 50 VOICE frames from the engine with matching values, in order, about 20 ms apart (pacer check with a tolerance of ±10 ms per frame and a total of 1.0 ±0.2 s);
   - a final keyup=0;
   - a TEXT packet with TLV 0x08 containing "PARROT";
   - events in this order: talk_start tx, talk_end tx, talk_start rx, talk_end rx; Duration about 1 s.
6. Variants:
   - fake drop of 1 frame, which must produce a silence frame and Timeouts=1, and keep later frames aligned;
   - source address not equal to the peer, which is ignored.
7. Run under -race. Use placeholders only: callsign N0CALL and DMR ID 1234567 in identity, 192.0.2.x in non-dialled fixture addresses.

### E.3 Verification checklist

- [ ] `gofmt -l .` empty; `go vet ./...`
- [ ] `go test -race ./...` (including the internal/dv/** tests, ctl server/client tests with SO_PEERCRED, hostlists tests with httptest, helper dv tests with the fake runner, api tests with ctltest.Fake)
- [ ] `CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build ./cmd/...`, also with arm64 and amd64
- [ ] `make debs`; `dpkg -c` shows /usr/libexec/nodespot/nodespot-dv and nodespot-dv.service for all three architectures
- [ ] `go run ./cmd/nodespot-dv bench -fake -frames 200` exits 0
- [ ] `cd web && npm ci && npm run check && npm test && npm run build`
- [ ] `grep -rn "Analog_Bridge\|STFU\|dv-" internal cmd web/src` shows only ini/, backup, migration stubs and comments
- [ ] Placeholder scan: no real callsigns, IDs or IPs in new tests (only N0CALL, 1234567, 1997/1998/1999, 192.0.2.x, node2001)
- [ ] On a Pi (bookworm, armhf), manual:
  - enable from the UI; md380-emu and nodespot-dv are active and the legacy units are stopped;
  - `ss -ulpn` shows 34001 on 127.0.0.1 only;
  - the parrot echo is heard via the private node;
  - Now Calling shows PARROT;
  - disable stops nodespot-dv and md380-emu stays installed;
  - the real bench passes on armhf and on one arm64 board.
- [ ] Upgrade from 1.4.x with the previous stack configured: favorites, resume, networks.json and the private node survive; the cards show "Not yet available in this build".

### E.4 CHANGELOG (operator-visible only)

- Digital modes now run on the built-in nodespot engine; the previous stack is no longer used, and its services are stopped when you enable digital modes.
- "Enable digital modes" installs only the md380-emu vocoder (plus qemu-user-static on 64-bit and x86 systems). ARMv6 boards are not supported.
- BrandMeister, TGIF, DMR, YSF, P25 and NXDN show "Not yet available in this build". Your settings and favorites are kept.
- New Parrot test mode echoes your transmission back through the bridge node.
- nodespot downloads the YSF, P25, NXDN and DMR host lists itself, once a day, while digital modes are enabled.
- Removed: the the previous stack uninstall, BrandMeister/STFU and network credential pages, and the the previous stack service restart and rollback buttons.

### Decisions for the owner

1. Do not download DMRIds.dat; use the radioid.net list instead (C.4).
2. Rename the panel package, paths, SSE event, settings keys and web files in M1 (hard cut-over); old names are kept only in `internal/legacy`.
3. Uninstall/purge of the previous stack is deferred to M6; M1 only stops the legacy units.

### Critical Files for Implementation
- /home/user/wt-200/internal/dv/ctl/types.go (new; frozen contract shared by T1 and T3)
- /home/user/wt-200/internal/helper/dv.go (source of the keyring, marker and platform code to relocate into dv.go/dv_common.go before deletion)
- /home/user/wt-200/internal/api/handlers_dv.go (route set to rewrite onto ctl and helper dv-* ops)
- /home/user/wt-200/cmd/nodespot/main.go (wiring of ctl client, hostlists, resume tracker and demo fake)
- /home/user/wt-200/packaging/nfpm.yaml (adds the nodespot-dv binary and unit to all three architecture packages)

