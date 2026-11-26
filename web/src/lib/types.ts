export type Role = 'admin' | 'operator' | 'viewer';
export interface User { id: number; username: string; role: Role }
/** An AllStar node list entry (favorites and calls of the AllStar mode). */
export interface NodeInfo { node: string; callsign: string; description: string; location: string }
export interface UpdateInfo {
  available: boolean; reason: string | null; applyReason: string | null; channel: 'stable' | 'beta'; installed: string; candidate: string; upgradable: boolean;
  job: 'idle' | 'running' | 'succeeded' | 'failed' | 'noop'; jobLog: string; notes: string | null; checked: string | null;
  /** Operating system (apt) updates; absent when the server cannot offer them. */
  os?: OSUpdates;
}
export interface OSPackage { name: string; from: string; to: string; origin?: string; security: boolean }
export interface OSUpdates {
  available: boolean; reason: string | null; checkedAt: string | null; checking: boolean; error: string | null;
  packages: OSPackage[]; keptBack: string[]; rebootRequired: boolean; rebootPkgs: string[];
  job: 'idle' | 'running' | 'succeeded' | 'failed' | 'noop'; jobLog: string;
  /** Non-fatal notes of the last check (e.g. a package repository could not be refreshed). */ warnings?: string[];
}
/** Admin-only SSE event "updates": counts only. */
export interface UpdatesBadge { nodespot: boolean; nodespotVersion: string; os: number; security: number; reboot: boolean }

// ---- The node: one radio, one active mode (GET /api/node, SSE "node").

/** The state of a mode's link. */
export type ModeState = 'idle' | 'starting' | 'ready' | 'connecting' | 'connected' | 'error';
export interface Target { id: string; name?: string; host?: string; port?: number; slot?: number; private?: boolean }
export interface Talker { callsign?: string; name?: string; place?: string; id?: number; target?: string; since: string }
export interface Registration {
  registered: boolean; ready: boolean; publicIp?: string; dnsOk: boolean;
  lastAttempt?: string; lastSuccess?: string; since?: string; lastError?: string; httpStatus?: number;
}
/** Counters of the current AllStar link, for diagnosing audio problems. */
export interface LinkStats {
  rxVoiceFull: number; rxVoiceMini: number; txVoice: number; vnakSent: number; vnakRecv: number;
  retransmits: number; parseErrors: number; dropped: number; gaps: number;
}
export interface ModeStatus {
  mode: string; title: string; active: boolean; state: ModeState; target?: Target; since?: string;
  detail?: string; error?: string; talker?: Talker; registration?: Registration; link?: LinkStats; links?: string[];
  /** The echo test's last level report. */
  levels?: EchoReport;
}
/** The level report of one echo test recording. Warnings are "clipping" and "too quiet". */
export interface EchoReport {
  at: string; frames: number; durationMs: number; peakDb: number; rmsDb: number; voicedFrames: number; clipped: number; warnings?: string[];
}
/** The live radio RX input level (GET /api/radio/input; also radio.level of the node status). */
export interface InputLevel { peakDb: number; rmsDb: number; cos: boolean }
export interface NodeJSON {
  callsign?: string; started: string;
  radio: {
    present: boolean; ptt: boolean; cos: boolean; rxLocked?: boolean; txLocked?: boolean;
    /** The preset in use and the interface found (chip name, USB id). */
    preset?: string; chip?: string; usbId?: string; product?: string; detected?: boolean; sa818?: boolean;
    level?: InputLevel;
  };
  /** The active mode ("" or absent when the node is idle) and its status. */
  active?: string; since?: string; mode?: ModeStatus; registration?: Registration;
  /** The last echo test report, when one was recorded. */
  echoTest?: EchoReport;
  id: { enabled: boolean; owed: boolean; due?: string; last?: string; why?: string };
  configured: boolean;
  /** False while the node daemon cannot be reached; `error` says why. */
  running: boolean; error?: string;
  /** True until the station has a callsign: the first-run setup opens. */
  setupNeeded: boolean;
}

export type FieldKind = 'text' | 'number' | 'password' | 'bool' | 'select';
export interface ModeField {
  key: string; label: string; kind: FieldKind; required?: boolean; secret?: boolean;
  default?: string; options?: string[]; help?: string; advanced?: boolean;
  /** The station value this field falls back to when left empty: "callsign", "dmrId" or "nxdnId". A required field with one is optional when the station has that value. */
  stationDefault?: 'callsign' | 'dmrId' | 'nxdnId';
  /** Appended to the station value shown as the placeholder (EchoLink uses "-L" after the callsign). */
  stationSuffix?: string;
  /** A text field the UI offers a host list picker for: "bm" (BrandMeister masters) or "dmr-other" (other Homebrew masters). The saved value stays plain text. */
  hostList?: 'bm' | 'dmr-other';
}
export interface ModeSettings { values?: Record<string, string>; favorites?: Target[]; reflectors?: Target[] }
/** One mode of GET /api/modes. Operators and viewers get no fields, settings or secretsSet. */
export interface ModeInfo {
  name: string; title: string; targetLabel?: string; targetHelp?: string; needsHost?: boolean;
  /** The mode's voice is AMBE: it needs the md380-emu vocoder (see Vocoder). */
  needsAmbe?: boolean;
  fields?: ModeField[]; settings?: ModeSettings; secretsSet?: Record<string, boolean>;
  /** Ready to use: every required value is set on the mode or comes from the station. The echo test is always ready. */
  configured: boolean;
  /** Keys of the required fields that have no value and no station fallback (admins). */
  missing?: string[];
  /** The mode has saved settings of its own. */
  saved?: boolean;
  /** The target the mode reconnects to when it becomes active (gone after an explicit disconnect). */
  last?: Target;
  active: boolean; status?: ModeStatus; error?: string;
}

/** GET /api/vocoder and POST /api/vocoder/install (admin). */
export interface Vocoder {
  installed: boolean; active: boolean; needed: string[]; supported: boolean; reason?: string;
  /** The install job: absent when none ran, else running, done or failed. */
  job?: 'running' | 'done' | 'failed'; jobLog?: string;
}

/** GET and PUT /api/station (admin). */
/** station.id: the station ID sent on the radio. Unset or 0 is the node's default. Unknown keys survive a save. */
export interface StationId {
  enabled?: boolean; intervalS?: number; tailWindowS?: number; endQuietS?: number;
  wpm?: number; pitchHz?: number; levelDb?: number; voiceFile?: string;
  [key: string]: unknown;
}
/** station.advanced: timers and AGC. Unset or 0 is the node's default. Unknown keys (md380 and others) survive a save. */
export interface StationAdvanced {
  hangMs?: number; txTimeoutS?: number;
  modeAgc?: boolean; radioAgc?: boolean; agcTargetDb?: number; agcMaxDb?: number;
  /** Static gain, -20 to 20 dB: radio to network and network to radio. 0 or unset is none. */
  rxGainDb?: number; txGainDb?: number;
  /** Software 75 microsecond emphasis (off by default); plFilter unset is automatic: on, unless the radio is an SA818. */
  preEmphasis?: boolean; deEmphasis?: boolean; plFilter?: boolean;
  /** Carrier debounce before a receive counts (0 to 1000 ms) and extra hang time (0 to 2000 ms). */
  rxOnDelayMs?: number; txOffDelayMs?: number;
  [key: string]: unknown;
}
export interface Station {
  callsign?: string;
  /** Optional digital IDs of the station (DMR 1 to 16777215, NXDN 1 to 65535); unset or 0 is none. Modes use them when their own ID is empty. */
  dmrId?: number; nxdnId?: number;
  radio: {
    frequencyMHz?: number; tone?: string; squelch?: number; narrow?: boolean; rxVolume?: number; txVolume?: number;
    /** "", "shari", "uri" or "none"; empty is automatic. The settings below override the preset's; unset keeps the preset's. */
    preset?: string;
    pttGpio?: number; pttActiveLow?: boolean; cosSource?: string; cosActiveLow?: boolean;
    ctcssSource?: string; ctcssActiveLow?: boolean; sa818?: boolean; sa818Port?: string;
    /** Microphone boost of the sound chip, when it has the control. */
    rxBoost?: boolean;
    /** The SA818's filter flags, written to the module only by Program. Unset flags are on. */
    sa818Filter?: Sa818Filter;
  };
  id: StationId;
  advanced: StationAdvanced;
  active?: { mode: string; target?: Target };
}
/** The settings of a radio interface (a preset's defaults, or what runs). */
export interface RadioInterface {
  preset: string; audio: boolean; pttPin: number; pttActiveLow: boolean; cos: string; cosActiveLow: boolean;
  ctcss: string; ctcssActiveLow: boolean; sa818: boolean;
}
export interface Sa818Filter { emphasis?: boolean; highPass?: boolean; lowPass?: boolean }
/** What the SA818's filters were last programmed to (from its state file); absent when never programmed. */
export interface Sa818Programmed { emphasis: boolean; highPass: boolean; lowPass: boolean; at?: string }
export interface RadioPreset { name: string; title: string; description: string; defaults: RadioInterface }
/** GET /api/radio/info (admin). */
export interface RadioInfo {
  present: boolean; detected: boolean; chip?: string; usbId?: string; product?: string; busPath?: string; shari: boolean;
  interface: RadioInterface; preset: string; presets: RadioPreset[]; resolved?: RadioInterface; restartRequired?: boolean;
  sa818Filters?: Sa818Programmed;
}
export interface Reading { RMSDb: number; PeakDb: number; Active: boolean; Clipped: number; Frames: number }
/** GET and PUT /api/radio: the sound chip's levels. -1 means the chip has no such control. */
export interface RadioLevels {
  CaptureVolume: number; PlaybackVolume: number; BoostAvailable?: boolean; Boost?: boolean; Rx: Reading; Tx: Reading; DroppedRx: number; DroppedTx: number; Xruns: number;
}

/** One row of GET /api/heard: who was heard on which mode. */
export interface Heard {
  mode: string; callsign?: string; name?: string; place?: string; id?: number; target?: string;
  start: string; end?: string; durationNs?: number;
}

/** A reflector or master of GET /api/hostlists. */
export interface HostItem { value: string; name: string; detail?: string; address: string; port: number; password?: string }
export interface HostList { mode: string; items: HostItem[]; truncated?: boolean; etag: string }
/** One downloaded list in GET /api/hostlists/status: dmr, ysf, p25, nxdn or m17. */
export interface HostListState { entries: number; updated?: string; checked?: string; error?: string }
/** The AllStar node list in GET /api/hostlists/status. */
export interface NodelistState { count: number; updated: string | null; source: string; error: string | null; busy?: boolean }
/** GET /api/hostlists/status (and the answer of the refresh): nextManual is when the next manual refresh is allowed. */
export interface HostListsStatus { busy?: boolean; lists: Record<string, HostListState>; nextManual?: string; nodelist?: NodelistState }

/** A favorite: a saved target of one mode (GET /api/favorites). */
export interface Favorite { id: number; mode: string; target: string; label: string; group: string; position: number; pinned: boolean; info: NodeInfo | null }

/** The state of the nodespot-node systemd unit. */
export interface NodeService { available: boolean; service?: { unit: string; description: string; active: string; sub: string; enabled: string; since: string } }

export interface SystemInfo {
  hostname: string; cpuTemp: number | null; load: number[]; memTotal: number; memAvailable: number;
  uptime: number; node: { running: boolean; mode: string; uptime: number | null; error: string | null }; version: string;
  updateAvailable?: boolean; updateVersion?: string;
}
export interface About {
  version: string; nodeRunning: boolean; nodeStarted: string;
  platform: { os: string; arch: string; model: string; distro: string; kernel: string; hardware: string };
}
export type Settings = Record<string, unknown>

// ---- The public status view (GET /api/public/status, SSE "snapshot"): no IPs, no system info.
export interface PubStation { callsign: string; mode: string; modeTitle: string; target: string; state: ModeState | string; txKeyed: boolean; rxKeyed: boolean }
/** One call on the public page; callsign, name and location are absent when callsigns are hidden. */
export interface PubCall { mode: string; callsign?: string; target?: string; at: number; duration: number; name?: string; location?: string }
export interface PubSnapshot {
  title: string; now: number; callsigns: boolean; showHeard: boolean;
  station: PubStation; calling: PubCall | null; heard?: PubCall[];
}
export interface PublicInfo { enabled: boolean; title: string; loggedIn: boolean }
/** GET/PUT /api/public (admin). */
export interface PublicSettings {
  enabled: boolean; title: string; showCallsigns: boolean; showHeard: boolean; allowEmbedding: boolean; trustProxy: boolean;
  defaultTitle: string; clients: number;
}

// ---- Backup and restore (docs/BACKUP.md, /api/backup/*, /api/restore/*). Admin only (the restore upload also with a setup code).
export interface BackupInfo { node: boolean; secrets: boolean; sa818: boolean }
export interface BackupSelection { secrets: boolean }
export interface RestoreWarning { code: string; message: string }
export interface RestoreRadio { bandwidth: string; rxMHz: number; txMHz: number; rxTone: string; txTone: string; squelch: number }
export interface RestorePreview {
  backup: { hostname: string; created: string; createdBy: string; nodespotVersion: string; arch: string; osId?: string; osCodename?: string; osName?: string; callsign: string; channel: string };
  node: { arch: string; osId?: string; osCodename?: string; osName?: string; callsign: string };
  warnings: RestoreWarning[];
  users: { username: string; role: string }[];
  favorites: number; schedules: number;
  checklist: { item: string; value: string }[];
  radio: RestoreRadio | null;
  parts: { node: boolean; sa818: boolean; secrets: boolean; conf: boolean };
  /** The modes the restored node will have set up. */
  modes: string[];
  /** Modes left out: no password in the backup and none on this node. Set them up again after the restore. */
  missingSecrets: string[];
}
export interface RestoreUpload { id: string; preview: RestorePreview }
export interface RestoreStep { name: string; ok: boolean; detail: string }
export interface RestoreApplyResult { ok: boolean; steps: RestoreStep[]; restarting: boolean; error?: string }
export interface ConfigHealthCheck { name: string; ok: boolean; detail: string }
export interface RestoreResult {
  at: string; backupCreated: string; steps: RestoreStep[];
  health: { ok: boolean; pending?: boolean; checks: ConfigHealthCheck[] }; checklist: { item: string; value: string }[];
}

// ---- Schedules (docs/SCHEDULES.md): a window selects a mode with a target, and disconnects at its end.
export interface Schedule {
  id: number; name: string; enabled: boolean; mode: string; target: string;
  action: 'connect' | 'disconnect';
  /** Weekly: days of week, 0 = Sunday .. 6 = Saturday. Empty for a one-off. */
  days: number[];
  /** One-off: YYYY-MM-DD, else "". */
  date: string;
  start: string; end: string;
  lastRun: string | null; lastResult: string;
}
/** Body of POST/PUT /api/schedules: a Schedule without the server-side fields. */
export type ScheduleInput = Omit<Schedule, 'id' | 'lastRun' | 'lastResult'>;
export interface ScheduleEvent { at: string; id: number; name: string; mode: string; target: string; action: 'connect' | 'disconnect' }
export type ManualAction = 'pause' | 'pause_today' | 'cancel';
/** Cancel mode: windows canceled by a manual action, while they would still be running. */
export interface CancelNotice { reason: string; at: string; until: string; ids: number[]; names: string[] }
export interface SchedulesSnapshot {
  enabled: boolean; pausedReason: string; pausedAt: string | null; resumeAt: string | null;
  manualAction: ManualAction; lastCancel: CancelNotice | null; timezone: string; now: string;
  schedules: Schedule[]; active: { id: number; start: string; end: string }[]; upcoming: ScheduleEvent[];
}

// System tab (docs/API.md "System"); all admin-only.
export interface HostStatus { hostname: string; prettyHostname: string; os: string; hostnameAvailable: boolean; hostnamectlAvailable: boolean; hostnamectlMissing: boolean; timezone: string; ntp: boolean; ntpSynchronized: boolean; canNtp: boolean; localRtc: boolean; timeAvailable: boolean; now: string; clock: string }
export interface HostPatch { hostname?: string; timezone?: string; ntp?: boolean }
export interface SysLogs { unit: string; lines: number; since: string; text: string; truncated: boolean; available: boolean }
export interface SysService { unit: string; description: string; active: string; sub: string; enabled: string; since: string }
export interface SysServices { available: boolean; services: SysService[] }
export interface NetAddr { family: string; addr: string; prefix: number; scope: string; dynamic: boolean }
export interface NetIface { name: string; state: string; kind: string; mac: string; mtu: number; addrs: NetAddr[] }
export interface NetRoute { family: string; gateway: string; dev: string; metric: number }
export interface SysNetwork { available: boolean; interfaces: NetIface[]; gateways: NetRoute[]; dns: string[]; dnsSource: string }
export interface DiagItem { name: string; desc: string }
export interface DiagPlan { items: DiagItem[]; excluded: string[]; redacted: string[]; maxBytes: number; timeLimit: number; available: boolean; maskDefault: boolean }
export interface AuditEntry { id: number; time: string; user: string; action: string; detail: string }
export interface AuditPage { entries: AuditEntry[]; next: number | null }

// WiFi manager of the System tab (docs/API.md "WiFi").
export interface WifiDevice { name: string; type: string; state: string; connection: string; addrs: string[] }
export interface WifiHotspot { enabled: boolean; delaySeconds: number; active: boolean; ssid: string; address: string; firewall: string }
export interface WifiNetwork { id: string; name: string; ssid: string; priority: number; autoconnect: boolean; active: boolean; hidden: boolean; secured: boolean }
export interface WifiAP { ssid: string; signal: number; channel: number; security: string; secured: boolean; supported: boolean; inUse: boolean; saved: boolean }
export interface WifiState {
  available: boolean; reason?: string; hasWifi: boolean; radio: string; devices: WifiDevice[]; active: string; ethernet: boolean;
  hotspot: WifiHotspot; networks: WifiNetwork[]; viaWifi: boolean; viaHotspot: boolean;
}
export interface WifiScan { networks: WifiAP[]; note?: string }

// System > Terminal (docs/API.md "Browser terminal"); admin only, off by default.
export interface TerminalStatus { enabled: boolean; available: boolean; sessions: number; maxSessions: number; idleMinutes: number; maxHours: number }
export interface TerminalTicket { ticket: string; expiresIn: number }
// One live terminal session (attached or detached); GET /system/terminal/sessions.
export interface TerminalSessionInfo { id: string; owner: string; mine: boolean; started: number; attached: boolean; detachedSince?: number; linuxUser?: string }

/** One call of the Now Calling strip (GET /api/nowcalling, SSE "nowcalling"). `kind` is the mode name. */
export interface NowCall {
  id: string; kind: string; call?: string; first?: string; name?: string;
  city?: string; state?: string; country?: string; location?: string; dmrId?: string;
  /** AllStar: the node and its node list entry. */
  node?: string; desc?: string;
  /** The talkgroup, room or reflector, and its name when known. */
  target?: string; targetName?: string;
  /** Where the name and place came from when not the radioid.net list ("hamdb.org"). */ source?: string;
  start: string; end?: string; active: boolean;
}
export interface NowCalling { current: NowCall | null; recent: NowCall[] }
/** The hamdb.org callsign lookup (admin, digital modes settings): off by default. `cached` answers are kept, `today` requests were made in the last 24 hours (at most `dailyMax`); `pausedUntil` while a back-off holds requests back. */
export interface HamDBStatus { enabled: boolean; cached: number; today: number; dailyMax: number; pausedUntil?: string }
/** The radioid.net user list (admin, digital modes settings). */
export interface UserListStatus { enabled: boolean; entries: number; updated?: string; checked?: string; error?: string; busy?: boolean; size?: number }

/** SSE "nodeevent": something a mode reported (a talker started or ended, a state change, an error, text). */
export interface NodeEvent {
  kind: 'talk_start' | 'talk_end' | 'state' | 'error' | 'text'; mode: string; at: string;
  talker?: Talker; state?: ModeState; target?: Target; text?: string; error?: string;
}
