// In-memory implementation of docs/API.md for `npm run dev:mock`. Dev only; never part of the build.
// One node, one radio, one active mode. Every callsign, ID, host and number is a placeholder.
import type { Plugin } from 'vite';
import type { IncomingMessage, ServerResponse } from 'node:http';
import { handleBackup, downUntil } from './backup.ts';
import { createSchedules } from './schedules.ts';
import { handleSystem } from './system.ts';
import { handleWifi } from './wifi.ts';
import { AMBE_MODES, handleVocoder } from './vocoder.ts';

type Info = { node: string; callsign: string; description: string; location: string };
const places = ['Exampleton, ST', 'Sampleburg, ST', 'Testville, ST', 'Demoton, ST', 'Mockville, ST'];
const descs = ['Hub', 'Repeater 146.520', 'Simplex node', 'Net control', 'Club repeater', 'Linked UHF'];
const NODES: Info[] = Array.from({ length: 12 }, (_, i) => ({ node: String(1000 + i * 37), callsign: 'TEST' + String(i + 1).padStart(2, '0'), description: `${descs[i % descs.length]} (fake)`, location: places[i % places.length] }));
const byNode = new Map(NODES.map((n) => [n.node, n]));
const allstarInfo = (n: string) => byNode.get(n) ?? null;

// ---- Modes. Fields are what the settings forms draw; secrets are never returned, only whether they are set.
type Field = { key: string; label: string; kind: 'text' | 'number' | 'password' | 'bool' | 'select'; required?: boolean; secret?: boolean; default?: string; options?: string[]; help?: string; advanced?: boolean; stationDefault?: 'callsign' | 'dmrId' | 'nxdnId'; stationSuffix?: string };
interface ModeDef { name: string; title: string; targetLabel: string; targetHelp: string; needsHost?: boolean; fields: Field[]; targets: string[] }
const cs = (suffix?: string, required = true): Field => ({ key: 'callsign', label: suffix ? 'EchoLink callsign' : 'Callsign', kind: 'text', required, stationDefault: 'callsign', ...(suffix ? { stationSuffix: suffix } : {}), ...(required ? {} : { advanced: true }) });
const pw = (key: string, label: string, required = true): Field => ({ key, label, kind: 'password', secret: true, required });
const MODES: ModeDef[] = [
  { name: 'allstar', title: 'AllStar', targetLabel: 'Node number', targetHelp: 'An AllStar node number, for example 1000.', targets: ['1000', '1037', '1185'],
    fields: [{ key: 'node', label: 'Node number', kind: 'text', required: true, default: '1999' }, pw('password', 'Node password'), { key: 'server', label: 'Registration server', kind: 'text', advanced: true, default: 'register.example.net' }] },
  { name: 'echolink', title: 'EchoLink', targetLabel: 'Callsign or node', targetHelp: 'An EchoLink node number or callsign with -L or -R.', targets: ['N1CALL-L', '123456'],
    fields: [cs('-L'), pw('password', 'EchoLink password'), { key: 'location', label: 'Location text', kind: 'text', advanced: true }] },
  { name: 'bm', title: 'BrandMeister', targetLabel: 'Talkgroup', targetHelp: 'A talkgroup number, for example 91.', targets: ['91', '3100', '9'],
    fields: [{ key: 'id', label: 'DMR ID', kind: 'number', required: true, stationDefault: 'dmrId' }, pw('password', 'Hotspot security password'), { key: 'master', label: 'Master', kind: 'text', default: 'master.example.net', advanced: true }] },
  { name: 'dmr', title: 'DMR (other master)', targetLabel: 'Talkgroup', targetHelp: 'A talkgroup on the master you set up.', targets: ['9', '1'],
    fields: [{ key: 'id', label: 'DMR ID', kind: 'number', required: true, stationDefault: 'dmrId' }, { key: 'master', label: 'Master host', kind: 'text', required: true, default: 'dmr.example.net' }, { key: 'port', label: 'Master port', kind: 'number', default: '62031' }, pw('password', 'Master password')] },
  { name: 'tgif', title: 'TGIF', targetLabel: 'Talkgroup', targetHelp: 'A TGIF talkgroup number.', targets: ['31665', '9'],
    fields: [{ key: 'id', label: 'DMR ID', kind: 'number', required: true, stationDefault: 'dmrId' }, pw('password', 'TGIF password')] },
  { name: 'ysf', title: 'YSF', targetLabel: 'Reflector', targetHelp: 'A YSF reflector.', needsHost: true, targets: ['YSF-TEST'],
    fields: [cs(), { key: 'suffix', label: 'Suffix', kind: 'text', advanced: true }] },
  { name: 'p25', title: 'P25', targetLabel: 'Talkgroup', targetHelp: 'A P25 reflector talkgroup.', needsHost: true, targets: ['10100'],
    fields: [cs(), { key: 'id', label: 'DMR ID', kind: 'number', required: true, stationDefault: 'dmrId' }] },
  { name: 'nxdn', title: 'NXDN', targetLabel: 'Talkgroup', targetHelp: 'An NXDN reflector talkgroup.', needsHost: true, targets: ['65000'],
    fields: [cs(), { key: 'id', label: 'NXDN ID', kind: 'number', required: true, stationDefault: 'nxdnId' }] },
  { name: 'echotest', title: 'Echo test', targetLabel: 'Target', targetHelp: 'The echo test has no target.', targets: [],
    fields: [{ key: 'codec', label: 'Codec', kind: 'select', options: ['plain', 'ambe', 'imbe', 'codec2', 'gsm', 'ulaw'], default: 'plain', help: 'What the recording goes through before it is played back. Plain is the radio audio unchanged; ambe needs the vocoder.' },
      { key: 'playback_delay_ms', label: 'Playback delay (ms)', kind: 'number', advanced: true, default: '1000' }] },
  { name: 'm17', title: 'M17', targetLabel: 'Reflector and module', targetHelp: 'A reflector and module, for example M17-XXX C.', needsHost: true, targets: ['M17-XXX C'],
    fields: [cs(), { key: 'module', label: 'Module', kind: 'select', options: ['A', 'B', 'C', 'D'], default: 'C' }] },
];
const ambeNeeded = (d: ModeDef) => AMBE_MODES.includes(d.name) || (d.name === 'echotest' && (values.echotest?.codec ?? 'plain').toLowerCase() === 'ambe');
const defOf = (n: string) => MODES.find((m) => m.name === n);
const values: Record<string, Record<string, string>> = {
  allstar: { node: '1999', server: 'register.example.net' }, bm: { id: '1234567', master: 'master.example.net' },
};
const secrets: Record<string, Record<string, boolean>> = { allstar: { password: true }, bm: { password: true } };
// The station value a field falls back to ("" when the station has none).
const stationVal = (f: Field) => (f.stationDefault && station[f.stationDefault] ? String(station[f.stationDefault]) : '');
// The required fields with no value of their own, no default and no station fallback.
const missingOf = (m: ModeDef) => m.fields.filter((f) => f.required && !(f.secret ? secrets[m.name]?.[f.key] : (values[m.name]?.[f.key] ?? f.default ?? '') !== '') && !stationVal(f));
const isConfigured = (m: ModeDef) => missingOf(m).length === 0;
// What each mode remembers: the last target, or an explicit unlink. It is reconnected when the mode becomes active.
const tunes: Record<string, { target?: string; unlinked?: boolean }> = { bm: { target: '91' } };

const HOSTS: Record<string, { value: string; name: string; detail?: string; address: string; port: number }[]> = {
  ysf: [{ value: 'YSF-TEST', name: 'Test reflector', detail: 'Placeholder', address: 'ysf1.example.net', port: 42000 }, { value: 'YSF-DEMO', name: 'Demo reflector', address: 'ysf2.example.net', port: 42000 }],
  p25: [{ value: '10100', name: 'Test talkgroup', address: 'p25.example.net', port: 41000 }],
  nxdn: [{ value: '65000', name: 'Test talkgroup', address: 'nxdn.example.net', port: 41400 }],
  m17: [{ value: 'M17-XXX C', name: 'Test reflector', address: 'm17.example.net', port: 17000 }, { value: 'M17-YYY A', name: 'Second reflector', address: 'm17b.example.net', port: 17000 }],
};

// ---- Station and node state.
const station: Record<string, any> & { callsign: string; radio: Record<string, any>; id: Record<string, any> } = { callsign: 'N0CALL', dmrId: 1234567, radio: { frequencyMHz: 146.52, tone: '100.0', squelch: 4, narrow: false, rxVolume: 60, txVolume: 55, preset: '', sa818Port: '/dev/ttyUSB0' }, id: { enabled: true, intervalS: 600, wpm: 20, pitchHz: 800, levelDb: -12 } };
let stationSet = process.env.MOCK_STATION_SETUP !== '1';
if (!stationSet) { station.callsign = ''; delete station.dmrId; }
const radioPresent = process.env.MOCK_NO_RADIO !== '1';
// The radio interface presets, as the node lists them. MOCK_NO_RADIO=1 is a node with no USB interface.
const iface = (preset: string, audio: boolean, o: Record<string, unknown>) => ({ preset, audio, pttPin: 0, pttActiveLow: false, cos: 'none', cosActiveLow: false, ctcss: 'none', ctcssActiveLow: false, sa818: false, ...o });
const presets = [
  { name: 'shari', title: 'SHARI', description: 'The SHARI board: a CM108B sound chip and an SA818 radio module. PTT on GPIO3, carrier from GPIO2 (volume down) present when high, no CTCSS input. The node programs the SA818.', defaults: iface('shari', true, { pttPin: 3, cos: 'gpio2', sa818: true }) },
  { name: 'uri', title: 'USB interface (URI-style)', description: 'A CM108 or CM119 USB interface wired to an external radio, such as a URI-style or RA-style board. PTT on GPIO3, carrier from GPIO2 (volume down) present when low, no CTCSS input. Set the frequency and tones on the radio.', defaults: iface('uri', true, { pttPin: 3, cos: 'gpio2', cosActiveLow: true }) },
  { name: 'none', title: 'None (radioless)', description: 'No radio. The node links modes with no audio to or from a radio.', defaults: iface('none', false, {}) },
];
const usbChip = radioPresent ? { chip: 'CM108B', usbId: '0d8c:0012', product: 'USB Audio Device' } : {};
/** The settings the saved station would run with (the mock applies a save at once). */
const resolved = () => {
  const r = station.radio as Record<string, any>;
  const name = r.preset || (radioPresent ? 'shari' : 'none');
  const d = presets.find((x) => x.name === name)!.defaults;
  if (!d.audio) return { ...d };
  return { ...d, pttPin: r.pttGpio || d.pttPin, pttActiveLow: r.pttActiveLow ?? d.pttActiveLow, cos: r.cosSource || d.cos, cosActiveLow: r.cosActiveLow ?? d.cosActiveLow, ctcss: r.ctcssSource || d.ctcss, ctcssActiveLow: r.ctcssActiveLow ?? d.ctcssActiveLow, sa818: r.sa818 ?? d.sa818 };
};
const started = new Date(Date.now() - 5 * 3600e3).toISOString();
let active = 'allstar', target = '1000', state: 'idle' | 'connecting' | 'connected' = 'connected', since = new Date(Date.now() - 1800e3).toISOString();
let nodeRunning = true, ptt = false, cos = false;
let talker: { callsign: string; name: string; place: string; id?: number; since: string } | null = null;

interface Call { callsign: string; name: string; place: string; id?: number; mode: string; target: string; node?: string; desc?: string }
const CALLERS: Call[] = [
  { callsign: 'TEST01', name: 'Pat Example', place: 'Exampleton, ST', mode: 'allstar', target: '1000', node: '1000', desc: 'Hub (fake)' },
  { callsign: 'TEST02', name: 'Sam Example', place: 'Sampleburg, ST', mode: 'allstar', target: '1037', node: '1037', desc: 'Repeater (fake)' },
  { callsign: 'N0CALL', name: 'Pat Example', place: 'Springfield, XX', id: 1234567, mode: 'bm', target: '91' },
  { callsign: 'N1CALL', name: 'Sam Example', place: 'Shelbyville, XX', id: 1234568, mode: 'bm', target: '3100' },
  { callsign: 'N1CALL', name: 'Sam Example', place: 'Shelbyville, XX', mode: 'ysf', target: 'YSF-TEST' },
  { callsign: 'N0CALL', name: 'Pat Example', place: 'Springfield, XX', mode: 'm17', target: 'M17-XXX C' },
  { callsign: 'N1CALL-L', name: 'Sam Example', place: 'Shelbyville, XX', mode: 'echolink', target: 'N1CALL-L' },
];
interface HeardRow { mode: string; callsign: string; name: string; place: string; id?: number; target: string; start: number; end?: number }
const heard: HeardRow[] = CALLERS.slice(0, 6).map((c, i) => ({ mode: c.mode, callsign: c.callsign, name: c.name, place: c.place, id: c.id, target: c.target, start: Date.now() - (i + 1) * 420e3, end: Date.now() - (i + 1) * 420e3 + (4 + i) * 1000 }));
const now = () => Date.now();
const iso = (t: number) => new Date(t).toISOString();

const inputLevel = () => {
  const t = Date.now() / 1000, on = cos || Math.sin(t / 3) > 0.2;
  const peakDb = on ? -14 + 10 * Math.sin(t * 2.3) + Math.random() * 2 : -90;
  return { peakDb, rmsDb: on ? peakDb - 9 : -90, cos };
};
const nodeJSON = () => ({
  callsign: station.callsign || undefined, started,
  echoTest: { at: iso(now() - 30e3), frames: 160, durationMs: 3200, peakDb: -9.4, rmsDb: -21.8, voicedFrames: 142, clipped: 0 },
  radio: { present: radioPresent && resolved().audio, level: inputLevel(), ptt, cos, preset: resolved().preset, ...usbChip, detected: radioPresent, sa818: resolved().sa818 },
  active: active || undefined, since: active ? since : undefined,
  mode: active ? modeStatus(active) : undefined,
  id: { enabled: station.id.enabled, owed: false, last: iso(now() - 240e3) },
  configured: stationSet, running: nodeRunning, ...(nodeRunning ? {} : { error: 'dial unix /run/nodespot-node/control.sock: connect: no such file or directory' }),
  setupNeeded: !stationSet,
});
function modeStatus(name: string) {
  const d = defOf(name)!, on = name === active;
  return {
    mode: name, title: d.title, active: on, state: !on ? 'idle' : state === 'idle' ? 'ready' : state,
    ...(on && target ? { target: { id: target, ...(name === 'allstar' ? { name: allstarInfo(target)?.callsign } : {}) } } : {}), ...(on ? { since } : {}),
    ...(on && talker ? { talker: { callsign: talker.callsign, name: talker.name, place: talker.place, id: talker.id, since: talker.since } } : {}),
    ...(on && name === 'allstar' && target ? { link: { rxVoiceFull: 4, rxVoiceMini: 1250, txVoice: 0, vnakSent: 0, vnakRecv: 0, retransmits: 0, parseErrors: 0, dropped: 0, gaps: 0 } } : {}),
    ...(name === 'allstar' || name === 'echolink' ? { registration: { registered: isConfigured(d), ready: isConfigured(d), dnsOk: true, publicIp: '203.0.113.7', lastSuccess: iso(now() - 600e3) } } : {}),
  };
}
const modeList = (role: string) => MODES.map((d) => ({
  name: d.name, title: d.title, targetLabel: d.targetLabel, targetHelp: d.targetHelp, needsHost: d.needsHost, ...(ambeNeeded(d) ? { needsAmbe: true } : {}),
  ...(role === 'admin' ? { fields: d.fields, settings: { values: Object.fromEntries(Object.entries(values[d.name] ?? {})) }, secretsSet: secrets[d.name] ?? {} } : {}),
  configured: isConfigured(d), ...(missingOf(d).length ? { missing: missingOf(d).map((f) => f.key) } : {}), saved: !!values[d.name] || !!secrets[d.name],
  ...(tunes[d.name]?.target ? { last: { id: tunes[d.name].target } } : {}), active: d.name === active, status: modeStatus(d.name),
}));

// ---- Favorites.
let favs: { id: number; mode: string; target: string; label: string; group: string; position: number; pinned: boolean }[] = [
  { id: 1, mode: 'allstar', target: '1000', label: 'Main hub', group: 'Hubs', position: 0, pinned: true },
  { id: 2, mode: 'allstar', target: '1037', label: 'Club repeater', group: 'Hubs', position: 1, pinned: false },
  { id: 3, mode: 'bm', target: '91', label: 'World-wide', group: 'DMR', position: 0, pinned: true },
  { id: 4, mode: 'bm', target: '3100', label: 'USA', group: 'DMR', position: 1, pinned: false },
  { id: 5, mode: 'ysf', target: 'YSF-TEST', label: 'Test reflector', group: '', position: 0, pinned: false },
  { id: 6, mode: 'm17', target: 'M17-XXX C', label: 'M17 test', group: '', position: 0, pinned: false },
];
let nextFav = 7;
const sortedFavs = () => [...favs].sort((a, b) => a.mode.localeCompare(b.mode) || a.group.localeCompare(b.group) || a.position - b.position).map((f) => ({ ...f, info: f.mode === 'allstar' ? allstarInfo(f.target) : null }));

// ---- Now calling (the lookup strip) and the public view.
const callOf = (c: Call, active_: boolean, start: number, end?: number) => ({ id: `${c.mode}-${c.callsign}-${start}`, kind: c.mode, call: c.callsign, name: c.name, location: c.place, ...(c.id ? { dmrId: String(c.id) } : {}), ...(c.node ? { node: c.node, desc: c.desc } : {}), target: c.target, start: iso(start), ...(end ? { end: iso(end) } : {}), active: active_ });
const callFor = (h: HeardRow): Call => ({ callsign: h.callsign, name: h.name, place: h.place, id: h.id, mode: h.mode, target: h.target });
const nowCalling = () => ({
  current: talker ? callOf({ callsign: talker.callsign, name: talker.name, place: talker.place, id: talker.id, mode: active, target }, true, Date.parse(talker.since)) : null,
  recent: heard.filter((h) => h.end).sort((a, b) => b.start - a.start).slice(0, 10).map((h) => callOf(callFor(h), false, h.start, h.end)),
});
const heardJSON = () => [...heard].sort((a, b) => b.start - a.start).map((h) => ({ mode: h.mode, callsign: h.callsign, name: h.name, place: h.place, ...(h.id ? { id: h.id } : {}), target: h.target, start: iso(h.start), ...(h.end ? { end: iso(h.end), durationNs: (h.end - h.start) * 1e6 } : {}) }));

const pubCfg = { enabled: true, title: '', showCallsigns: true, showHeard: true, allowEmbedding: false, trustProxy: false };
const pubDefaultTitle = () => `${station.callsign || 'Station'} node`;
function pubSnapshot() {
  const cs = pubCfg.showCallsigns, d = defOf(active);
  const row = (c: { callsign: string; name: string; place: string; mode: string; target: string }, at: number, dur: number) => ({ mode: c.mode, ...(cs ? { callsign: c.callsign, name: c.name, location: c.place } : {}), target: c.target, at: Math.floor(at / 1000), duration: dur });
  return {
    title: pubCfg.title || pubDefaultTitle(), now: Math.floor(now() / 1000), callsigns: cs, showHeard: pubCfg.showHeard,
    station: { callsign: cs ? station.callsign : '', mode: active, modeTitle: d?.title ?? '', target, state: active ? state : 'idle', txKeyed: ptt, rxKeyed: cos },
    calling: talker ? row({ callsign: talker.callsign, name: talker.name, place: talker.place, mode: active, target }, Date.parse(talker.since), 0) : null,
    ...(pubCfg.showHeard ? { heard: heard.filter((h) => h.end).sort((a, b) => b.start - a.start).slice(0, 15).map((h) => row(h, h.start, Math.round((h.end! - h.start) / 1000))) } : {}),
  };
}

// ---- Radio levels (the sound chip) and the caller lookup lists.
const level = (on: boolean) => ({ RMSDb: on ? -24 + Math.random() * 6 : -60, PeakDb: on ? -12 + Math.random() * 4 : -55, Active: on, Clipped: 0, Frames: 480 });
const radioLevels = () => ({ CaptureVolume: station.radio.rxVolume, PlaybackVolume: station.radio.txVolume, Rx: level(cos), Tx: level(ptt), DroppedRx: 0, DroppedTx: 0, Xruns: 0 });
let userList = { enabled: true, entries: 183204, updated: iso(now() - 86400e3), checked: iso(now() - 3600e3), size: 9.4e6, busy: false };
let hamdb = { enabled: false, cached: 0, today: 0, dailyMax: 500 };

let users = [{ id: 1, username: 'admin', role: 'admin' }, { id: 2, username: 'op', role: 'operator' }, { id: 3, username: 'guest', role: 'viewer' }];
let nextUser = 4;
let setupNeeded = process.env.MOCK_SETUP === '1';
// Simulated update: apply runs ~6s, then the "server" restarts (SSE streams close) and reports the new version.
let mockVersion = '2.0.0';
let mockInstance = Math.random().toString(16).slice(2, 10);
let mockChannel: 'stable' | 'beta' = 'stable';
const candidate = () => (mockChannel === 'beta' ? '2.1.0~beta.1' : '2.0.1');
let updJob: 'idle' | 'running' | 'succeeded' = 'idle';
let updChecked: string | null = null;
let restartClients: () => void = () => {};
const upgradable = () => updChecked !== null && mockVersion !== candidate();
const updInfo = () => ({
  os: osInfo(),
  available: true, reason: null, applyReason: null, channel: mockChannel, installed: mockVersion, candidate: updChecked ? candidate() : '', upgradable: upgradable(), job: updJob,
  jobLog: updJob === 'running' ? 'Reading package lists...\nUnpacking nodespot (0.2.0) over (0.1.0) ...' : updJob === 'succeeded' ? 'Setting up nodespot (0.2.0) ...' : '',
  notes: upgradable() ? '- New **Updates** section in Settings\n- Faster dashboard\n\nThanks for testing <script>alert(1)</script>' : null, checked: updChecked,
});
// Simulated OS (apt) updates: a security package, an ordinary package, a reboot-required toggle.
const osAll = [
  { name: 'ffmpeg', from: '7:5.1.5-1', to: '7:5.1.6-1', security: false },
  { name: 'libssl3', from: '3.0.11-1~deb12u1', to: '3.0.13-1~deb12u2', security: true },
  { name: 'linux-image-rpi-v8', from: '6.1.63-1+rpt1', to: '6.1.69-1+rpt1', security: false },
  { name: 'tzdata', from: '2024a-0+deb12u1', to: '2024b-0+deb12u1', security: false },
];
let osChecked: string | null = new Date().toISOString(), osChecking = false, osJob: 'idle' | 'running' | 'succeeded' = 'idle', osApplied = false, osReboot = false;
const osPkgs = () => (osApplied ? [] : osAll);
const osInfo = () => ({
  available: true, reason: null, checkedAt: osChecked, checking: osChecking, error: null, packages: osPkgs(), keptBack: osApplied ? [] : ['raspi-firmware'],
  rebootRequired: osReboot, rebootPkgs: osReboot ? ['linux-image-rpi-v8'] : [], job: osJob,
  jobLog: osJob === 'running' ? 'Reading package lists...\nUnpacking libssl3 (3.0.13-1~deb12u2) over (3.0.11-1~deb12u1) ...' : osJob === 'succeeded' ? 'Setting up libssl3 (3.0.13-1~deb12u2) ...\nSetting up ffmpeg (7:5.1.6-1) ...' : '',
});
const osBadge = () => ({ nodespot: upgradable(), nodespotVersion: upgradable() ? candidate() : '', os: osPkgs().length, security: osPkgs().filter((p) => p.security).length, reboot: osReboot });
const system = () => ({ updateAvailable: upgradable(), updateVersion: upgradable() ? candidate() : '', hostname: 'node-demo', cpuTemp: 44 + Math.random() * 6, load: [0.12, 0.1, 0.05].map((x) => x + Math.random() * 0.1), memTotal: 1024e6, memAvailable: 512e6, uptime: 120660 + Math.floor(process.uptime()), node: { running: nodeRunning, mode: active, uptime: nodeRunning ? 18000 : null, error: nodeRunning ? null : 'control socket unreachable' }, version: mockVersion });

export function mockApi(): Plugin {
  const sessions = new Map<string, { id: number; username: string; role: string }>();
  const clients = new Set<{ res: ServerResponse; role: string }>();
  const json = (res: ServerResponse, code: number, body?: unknown) => {
    res.statusCode = code;
    if (body !== undefined) { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(body)); } else res.end();
  };
  const err = (res: ServerResponse, code: number, msg: string, c: string) => json(res, code, { error: msg, code: c });
  let rawBody = '';
  const readBody = (req: IncomingMessage) => new Promise<any>((ok) => { let d = ''; req.on('data', (c) => (d += c.toString('latin1'))); req.on('end', () => { rawBody = d; try { ok(d ? JSON.parse(d) : {}); } catch { ok({}); } }); });
  const pubClients = new Set<ServerResponse>();
  const sse = (c: { res: ServerResponse }, ev: string, data: unknown) => c.res.write(`event: ${ev}\ndata: ${JSON.stringify(data)}\n\n`);
  restartClients = () => { clients.forEach((c) => c.res.end()); clients.clear(); };
  const broadcast = (ev: string, f: (role: string) => unknown) => clients.forEach((c) => { const d = f(c.role); if (d !== undefined) sse(c, ev, d); });
  const pushNode = () => broadcast('node', () => nodeJSON());
  const event = (kind: string, extra: Record<string, unknown> = {}) => broadcast('nodeevent', () => ({ kind, mode: active, at: iso(now()), ...extra }));

  // The one active mode: select it, optionally connect a target (a connect takes a moment).
  const selectMode = (mode: string, t?: string) => {
    active = mode; talker = null; cos = false;
    since = iso(now());
    if (mode && t) tunes[mode] = { target: t };
    else if (mode && !tunes[mode]?.unlinked && tunes[mode]?.target) t = tunes[mode].target;
    if (!mode) { target = ''; state = 'idle'; } else if (t) { target = t; state = 'connecting'; setTimeout(() => { if (active === mode && target === t) { state = 'connected'; pushNode(); event('state', { state: 'connected' }); } }, 1200); } else { target = ''; state = 'idle'; }
    pushNode();
  };
  const sched = createSchedules({
    current: () => ({ mode: active, target: state === 'idle' ? '' : target }),
    link: (mode, t) => selectMode(mode, t),
    unlink: () => { target = ''; state = 'idle'; talker = null; pushNode(); },
    modes: () => MODES.map((m) => m.name),
    broadcast, pushNode,
  });

  return {
    name: 'nodespot-mock',
    configureServer(server) {
      let tick = 0;
      setInterval(() => {
        tick++;
        // A talker on the active mode for 8 s of every 30 s, once it is connected.
        const want = active && state === 'connected' && nodeRunning && tick % 30 < 8;
        if (want && !talker) {
          const pool = CALLERS.filter((c) => c.mode === active); const c = pool[tick % Math.max(1, pool.length)] ?? CALLERS[0];
          talker = { callsign: c.callsign, name: c.name, place: c.place, id: c.id, since: iso(now()) }; cos = true;
          heard.push({ mode: active, callsign: c.callsign, name: c.name, place: c.place, id: c.id, target, start: now() });
          event('talk_start', { talker });
        } else if (!want && talker) {
          const h = [...heard].reverse().find((x) => !x.end); if (h) h.end = now();
          const was = talker; talker = null; cos = false;
          event('talk_end', { talker: was });
          heard.splice(0, Math.max(0, heard.length - 100));
        }
        pushNode();
        broadcast('nowcalling', () => nowCalling());
        if (pubCfg.enabled) pubClients.forEach((res) => res.write(`event: snapshot\ndata: ${JSON.stringify(pubSnapshot())}\n\n`));
        if (tick % 10 === 0) broadcast('system', system);
      }, 1000);
      setInterval(() => clients.forEach((c) => c.res.write(': ping\n\n')), 15000);

      server.middlewares.use(async (req, res, next) => {
        const url = new URL(req.url ?? '/', 'http://x');
        if (!url.pathname.startsWith('/api/')) return next();
        if (Date.now() < downUntil) { req.socket.destroy(); return; } // simulated restart after a restore
        res.setHeader('X-nodespot-Instance', mockInstance);
        const p = url.pathname.slice(4), m = req.method ?? 'GET';
        const cookie = /nodespot_session=([^;]+)/.exec(req.headers.cookie ?? '')?.[1];
        const user = cookie ? sessions.get(cookie) : undefined;
        if (m !== 'GET' && req.headers['x-nodespot'] !== '1') return err(res, 403, 'missing X-nodespot header', 'csrf');
        const body = m === 'GET' ? {} : await readBody(req);
        const login = (u: { id: number; username: string; role: string }) => { const tok = Math.random().toString(36).slice(2); sessions.set(tok, u); res.setHeader('Set-Cookie', `nodespot_session=${tok}; Path=/; HttpOnly; SameSite=Strict`); };

        if (p === '/setup') {
          if (m === 'GET') return json(res, 200, { needed: setupNeeded, hotspot: process.env.MOCK_HOTSPOT === '1', station: !stationSet });
          if (!setupNeeded) return err(res, 409, 'setup has already been completed', 'setup_done');
          if (body.code !== '123456') return err(res, 403, 'wrong setup code (mock: 123456)', 'bad_setup_code');
          users = [{ id: 1, username: body.username, role: 'admin' }]; setupNeeded = false; login(users[0]); return json(res, 204);
        }
        if (p === '/login' && m === 'POST') {
          const u = users.find((x) => x.username === body.username);
          if (!u || body.password !== 'pass') return err(res, 401, 'invalid username or password', 'bad_credentials');
          login(u); return json(res, 200, u);
        }
        if (p === '/logout') { if (cookie) sessions.delete(cookie); return json(res, 204); }
        if (p === '/public/info') return json(res, 200, { enabled: pubCfg.enabled, title: pubCfg.enabled ? pubSnapshot().title : '', loggedIn: !!user });
        if (p === '/public/status') return pubCfg.enabled ? json(res, 200, pubSnapshot()) : err(res, 404, 'no such endpoint', 'not_found');
        if (p === '/public/events') {
          if (!pubCfg.enabled) return err(res, 404, 'no such endpoint', 'not_found');
          res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' });
          pubClients.add(res); req.on('close', () => pubClients.delete(res));
          res.write(`event: snapshot\ndata: ${JSON.stringify(pubSnapshot())}\n\n`);
          return;
        }
        if (handleBackup({ m, p, body, raw: rawBody, res, user, setupNeeded, setupDone: () => { setupNeeded = false; }, json: (c, b) => json(res, c, b), err: (c, msg, code) => json(res, c, { error: msg, code }), restart: () => restartClients() })) return;
        if (!user) return err(res, 401, 'authentication required', 'unauthorized');
        const isAdmin = user.role === 'admin', canOp = user.role !== 'viewer';
        const forbid = () => err(res, 403, 'forbidden', 'forbidden');
        let mm: RegExpExecArray | null;
        if (handleVocoder({ extra: () => (ambeNeeded(defOf('echotest')!) ? ['echotest'] : []), m, p, user, json: (c, b) => json(res, c, b), err: (c, msg, code) => json(res, c, { error: msg, code }) })) return;
        if (handleWifi({ m, p, url, body, user, json: (c, b) => json(res, c, b), err: (c, msg, code) => json(res, c, { error: msg, code }) })) return;
        if (handleSystem({ m, p, url, body, user, json: (c, b) => json(res, c, b), err: (c, msg, code) => json(res, c, { error: msg, code }), file: (name, text) => { res.statusCode = 200; res.setHeader('Content-Type', 'application/gzip'); res.setHeader('Content-Disposition', `attachment; filename="${name}"`); res.end(text); } })) return;

        if (p === '/session') return json(res, 200, user);
        if (p === '/password') { if (body.current !== 'pass') return err(res, 400, 'current password is wrong', 'bad_credentials'); return json(res, 204); }
        if (p === '/events') {
          res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' });
          const c = { res, role: user.role };
          clients.add(c); req.on('close', () => clients.delete(c));
          sse(c, 'hello', { user, version: mockVersion, instance: mockInstance, settings: {}, setupNeeded: !stationSet });
          sse(c, 'node', nodeJSON());
          sse(c, 'nowcalling', nowCalling());
          if (user.role === 'admin') sse(c, 'updates', osBadge());
          sse(c, 'system', system()); sse(c, 'favorites', sortedFavs()); sse(c, 'schedules', sched.snapshot());
          return;
        }

        // ---- The node: status, modes, station, the one active mode.
        if (p === '/node' && m === 'GET') return json(res, 200, nodeJSON());
        if (p === '/node/reload' && m === 'POST') { if (!isAdmin) return forbid(); return json(res, 200, nodeJSON()); }
        if (p === '/modes' && m === 'GET') return json(res, 200, modeList(user.role));
        if ((mm = /^\/modes\/([a-z0-9]+)$/.exec(p)) && (m === 'PUT' || m === 'DELETE')) {
          if (!isAdmin) return forbid();
          const d = defOf(mm[1]); if (!d) return err(res, 404, 'unknown mode', 'not_found');
          if (m === 'DELETE') { delete values[d.name]; delete secrets[d.name]; delete tunes[d.name]; return json(res, 204); }
          const v: Record<string, string> = (body.values ?? {}) as Record<string, string>;
          for (const f of d.fields) {
            if (!(f.key in v)) continue;
            if (f.secret) { if (v[f.key] !== '') (secrets[d.name] ??= {})[f.key] = true; } else (values[d.name] ??= {})[f.key] = String(v[f.key]);
          }
          const miss = d.fields.find((f) => f.required && !f.secret && (values[d.name]?.[f.key] ?? '') === '' && !f.default && !stationVal(f));
          if (miss) return err(res, 400, `${miss.label} is required`, 'bad_request');
          return json(res, 200, modeList(user.role));
        }
        if (p === '/station') {
          if (!isAdmin) return forbid();
          if (m === 'GET') return json(res, 200, { ...station, advanced: {}, active: active ? { mode: active, target: target ? { id: target } : undefined } : undefined });
          if (m === 'PUT') {
            if (!/^[A-Za-z0-9/]{3,12}$/.test(String(body.callsign ?? ''))) return err(res, 400, 'enter the station callsign', 'bad_request');
            const ids: [string, number][] = [['dmrId', 16777215], ['nxdnId', 65535]];
            for (const [k, max] of ids) {
              const n = Number(body[k] ?? 0);
              if (!Number.isInteger(n) || n < 0 || n > max) return err(res, 400, `${k === 'dmrId' ? 'DMR' : 'NXDN'} ID must be 1 to ${max}`, 'bad_request');
            }
            station.callsign = String(body.callsign).toUpperCase();
            for (const [k] of ids) { if (body[k]) station[k] = Number(body[k]); else delete station[k]; }
            if (body.radio) {
              for (const k of ['preset', 'pttGpio', 'pttActiveLow', 'cosSource', 'cosActiveLow', 'ctcssSource', 'ctcssActiveLow', 'sa818']) delete (station.radio as Record<string, unknown>)[k];
              Object.assign(station.radio, body.radio);
            }
            if (body.id) Object.assign(station.id, body.id);
            stationSet = true; pushNode(); return json(res, 200, nodeJSON());
          }
        }
        if (p === '/select' && m === 'POST') {
          if (!canOp) return forbid();
          const mode = String(body.mode ?? '');
          if (mode && !defOf(mode)) return err(res, 400, 'unknown mode', 'bad_request');
          if (mode && !isConfigured(defOf(mode)!)) return err(res, 409, `${defOf(mode)!.title} is not set up yet`, 'not_configured');
          sched.manual(`manual select ${mode || 'idle'} by ${user.username}`);
          selectMode(mode, body.target ? String(body.target) : undefined); return json(res, 200, nodeJSON());
        }
        if (p === '/connect' && m === 'POST') {
          if (!canOp) return forbid();
          if (!active) return err(res, 409, 'pick a mode first', 'no_mode');
          if (!String(body.target ?? '').trim()) return err(res, 400, 'a target is required', 'bad_request');
          sched.manual(`manual connect ${body.target} by ${user.username}`);
          selectMode(active, String(body.target)); return json(res, 200, nodeJSON());
        }
        if (p === '/disconnect' && m === 'POST') {
          if (!canOp) return forbid();
          sched.manual(`manual disconnect by ${user.username}`);
          if (active) tunes[active] = { unlinked: true };
          target = ''; state = 'idle'; talker = null; pushNode(); return json(res, 200, nodeJSON());
        }
        if (p === '/heard' && m === 'GET') return json(res, 200, heardJSON().slice(0, +(url.searchParams.get('limit') ?? 50)));
        if (p === '/radio' && (m === 'GET' || m === 'PUT')) {
          if (m === 'PUT') { if (!isAdmin) return forbid(); if (typeof body.rxVolume === 'number') station.radio.rxVolume = body.rxVolume; if (typeof body.txVolume === 'number') station.radio.txVolume = body.txVolume; }
          if (!radioPresent) return err(res, 404, 'this node has no radio', 'no_radio');
          return json(res, 200, radioLevels());
        }
        if (p === '/radio/input' && m === 'GET') {
          if (!isAdmin) return forbid();
          if (!radioPresent) return err(res, 404, 'this node has no radio', 'no_radio');
          return json(res, 200, inputLevel());
        }
        if (p === '/radio/info' && m === 'GET') {
          if (!isAdmin) return forbid();
          const r = resolved();
          return json(res, 200, { present: radioPresent && r.audio, detected: radioPresent, ...usbChip, shari: radioPresent, interface: r, preset: station.radio.preset ?? '', presets, resolved: r });
        }
        if (p === '/radio/program' && m === 'POST') { if (!isAdmin) return forbid(); return json(res, 200, nodeJSON()); }
        if (p === '/hostlists' && m === 'GET') {
          const mode = url.searchParams.get('mode') ?? '';
          return json(res, 200, { mode, items: HOSTS[mode] ?? [], etag: 'mock' });
        }
        if (p === '/hostlists/refresh' && m === 'POST') { if (!isAdmin) return forbid(); return json(res, 202); }
        if (p === '/system/node-service' && m === 'GET') {
          if (!isAdmin) return forbid();
          return json(res, 200, { available: true, service: { unit: 'nodespot-node.service', description: 'nodespot node', active: nodeRunning ? 'active' : 'failed', sub: nodeRunning ? 'running' : 'failed', enabled: 'enabled', since: 'Mon 2026-11-16 08:00:01 UTC' } });
        }
        if (p === '/system/node-service/restart' && m === 'POST') {
          if (!isAdmin) return forbid();
          nodeRunning = false; pushNode(); setTimeout(() => { nodeRunning = true; pushNode(); }, 3000);
          return json(res, 202, { ok: true });
        }

        // ---- Caller lookup lists.
        if (p === '/dv/userlist') {
          if (!isAdmin) return forbid();
          if (m === 'PUT') userList = { ...userList, enabled: !!body.enabled };
          if (m === 'POST' || p.endsWith('/download')) userList = { ...userList, updated: iso(now()) };
          return json(res, 200, userList);
        }
        if (p === '/dv/userlist/download' && m === 'POST') { if (!isAdmin) return forbid(); userList = { ...userList, updated: iso(now()) }; return json(res, 200, userList); }
        if (p === '/dv/hamdb') {
          if (!isAdmin) return forbid();
          if (m === 'PUT') hamdb = { ...hamdb, enabled: !!body.enabled };
          return json(res, 200, hamdb);
        }
        if (p === '/nowcalling' && m === 'GET') return json(res, 200, nowCalling());

        // ---- Public page settings.
        if (p === '/public') {
          if (!isAdmin) return forbid();
          if (m === 'PUT') {
            for (const k of ['enabled', 'showCallsigns', 'showHeard', 'allowEmbedding', 'trustProxy'] as const) if (typeof body[k] === 'boolean') pubCfg[k] = body[k];
            if (typeof body.title === 'string') { if (body.title.length > 80) return err(res, 400, 'title is too long', 'bad_request'); pubCfg.title = body.title; }
            if (!pubCfg.enabled) { pubClients.forEach((r) => r.end()); pubClients.clear(); }
          }
          return json(res, 200, { ...pubCfg, defaultTitle: pubDefaultTitle(), clients: pubClients.size });
        }
        if (p === '/settings' && m === 'GET') return json(res, 200, {});
        if (p === '/updates' || p.startsWith('/updates/')) {
          if (!isAdmin) return forbid();
          if (p === '/updates' && m === 'GET') return json(res, 200, updInfo());
          if (p === '/updates/check' && m === 'POST') { updChecked = new Date().toISOString(); return json(res, 200, updInfo()); }
          if (p === '/updates/channel' && m === 'PUT') {
            if (body.channel !== 'stable' && body.channel !== 'beta') return err(res, 400, 'channel must be "stable" or "beta"', 'bad_request');
            if (updJob === 'running') return err(res, 409, 'an update is already running', 'update_running');
            mockChannel = body.channel; updChecked = new Date().toISOString();
            return json(res, 200, updInfo());
          }
          if (p === '/updates/apply' && m === 'POST') {
            if (updJob === 'running') return err(res, 409, 'an update is already running', 'update_running');
            updJob = 'running';
            const target = candidate();
            setTimeout(() => { mockVersion = target; updJob = 'succeeded'; restartClients(); }, 6000);
            return json(res, 202, updInfo());
          }
        }
        if (p === '/updates/os/check' && m === 'POST') {
          if (!isAdmin) return forbid();
          if (osJob === 'running') return err(res, 409, 'an update is already running', 'update_running');
          osChecking = true; broadcast('updates', (r) => (r === 'admin' ? osBadge() : undefined));
          setTimeout(() => { osChecking = false; osChecked = new Date().toISOString(); broadcast('updates', (r) => (r === 'admin' ? osBadge() : undefined)); }, 2500);
          return json(res, 202, updInfo());
        }
        if (p === '/updates/os/apply' && m === 'POST') {
          if (!isAdmin) return forbid();
          if (osJob === 'running' || updJob === 'running') return err(res, 409, 'an update is already running', 'update_running');
          osJob = 'running';
          setTimeout(() => { osJob = 'succeeded'; osApplied = true; osReboot = true; osChecked = new Date().toISOString(); broadcast('updates', (r) => (r === 'admin' ? osBadge() : undefined)); }, 6000);
          return json(res, 202, updInfo());
        }
        if (p === '/system/reboot' && m === 'POST') {
          if (!isAdmin) return forbid();
          osReboot = false; broadcast('updates', (r) => (r === 'admin' ? osBadge() : undefined));
          setTimeout(() => restartClients(), 500);
          return json(res, 202, { ok: true });
        }
        if (p === '/system/restart-nodespot' && m === 'POST') { if (!isAdmin) return forbid(); setTimeout(() => { mockInstance = Math.random().toString(16).slice(2, 10); restartClients(); }, 500); return json(res, 202, { ok: true }); }
        if (p === '/users' || p.startsWith('/users/')) {
          if (!isAdmin) return forbid();
          if (p === '/users' && m === 'GET') return json(res, 200, users);
          if (p === '/users' && m === 'POST') {
            if (!body.username || !body.password) return err(res, 400, 'username and password required', 'bad_request');
            if (users.some((u) => u.username === body.username)) return err(res, 409, 'user exists', 'conflict');
            const u = { id: nextUser++, username: body.username, role: body.role || 'viewer' }; users.push(u); return json(res, 201, u);
          }
          if ((mm = /^\/users\/(\d+)$/.exec(p))) {
            const u = users.find((x) => x.id === +mm![1]); if (!u) return err(res, 404, 'not found', 'not_found');
            if (m === 'PUT') { if (body.role) { if (u.role === 'admin' && body.role !== 'admin' && users.filter((x) => x.role === 'admin').length < 2) return err(res, 409, 'cannot demote the last admin', 'last_admin'); u.role = body.role; } return json(res, 200, u); }
            if (m === 'DELETE') { if (u.id === user.id) return err(res, 409, 'cannot delete yourself', 'conflict'); users = users.filter((x) => x !== u); return json(res, 204); }
          }
        }
        if (p === '/about' && m === 'GET') return json(res, 200, { version: mockVersion, nodeRunning, nodeStarted: started, platform: { os: 'linux', arch: 'arm64', model: 'Raspberry Pi 4 Model B Rev 1.5', distro: 'Debian GNU/Linux 13 (trixie)', kernel: '6.12.0-rpi', hardware: 'SHARI SA818 (placeholder)' } });
        if (p === '/diagnostics' && m === 'GET') {
          if (!isAdmin) return forbid();
          res.statusCode = 200; res.setHeader('Content-Type', 'text/plain; charset=utf-8');
          res.setHeader('Content-Disposition', `attachment; filename="nodespot-diagnostics-${new Date().toISOString().slice(0, 10)}.txt"`);
          return res.end(`nodespot diagnostics (mock)\nnodespot: ${mockVersion}\nPassword = <redacted>\n`);
        }
        if (sched.handle(m, p, body, canOp, (c, b) => json(res, c, b), (c, msg, code) => err(res, c, msg, code), forbid)) return;

        // ---- Favorites: a saved target of one mode.
        if (p === '/favorites/reorder' && m === 'POST') {
          if (!canOp) return forbid();
          (body.ids as number[]).forEach((id, i) => { const f = favs.find((x) => x.id === id); if (f) f.position = i; });
          broadcast('favorites', sortedFavs); return json(res, 204);
        }
        if (p === '/favorites') {
          if (m === 'GET') { const mode = url.searchParams.get('mode'); return json(res, 200, sortedFavs().filter((f) => !mode || f.mode === mode)); }
          if (!canOp) return forbid();
          if (m === 'POST') {
            if (!defOf(body.mode ?? '')) return err(res, 400, 'unknown mode', 'bad_request');
            if (!String(body.target ?? '').trim()) return err(res, 400, 'a target is required', 'bad_request');
            if (favs.some((f) => f.mode === body.mode && f.target === body.target)) return err(res, 409, 'already a favorite', 'conflict');
            const f = { id: nextFav++, mode: body.mode, target: String(body.target).trim(), label: body.label || String(body.target), group: body.group || '', position: 99, pinned: false };
            favs.push(f); broadcast('favorites', sortedFavs); return json(res, 201, { ...f, info: f.mode === 'allstar' ? allstarInfo(f.target) : null });
          }
        }
        if ((mm = /^\/favorites\/(\d+)$/.exec(p))) {
          if (!canOp) return forbid();
          const f = favs.find((x) => x.id === +mm![1]); if (!f) return err(res, 404, 'not found', 'not_found');
          if (m === 'PUT') { for (const k of ['target', 'label', 'group'] as const) if (typeof body[k] === 'string') f[k] = body[k]; if (typeof body.pinned === 'boolean') f.pinned = body.pinned; broadcast('favorites', sortedFavs); return json(res, 200, { ...f, info: f.mode === 'allstar' ? allstarInfo(f.target) : null }); }
          if (m === 'DELETE') { favs = favs.filter((x) => x !== f); broadcast('favorites', sortedFavs); return json(res, 204); }
        }
        if (p === '/lookup' && m === 'GET') {
          const q = (url.searchParams.get('q') ?? '').toLowerCase(), lim = +(url.searchParams.get('limit') ?? 50);
          return json(res, 200, NODES.filter((n) => n.node.startsWith(q) || [n.callsign, n.description, n.location].some((s) => s.toLowerCase().includes(q))).slice(0, lim));
        }
        if ((mm = /^\/lookup\/(.+)$/.exec(p))) { const i = allstarInfo(decodeURIComponent(mm[1])); return i ? json(res, 200, i) : err(res, 404, 'node not found', 'not_found'); }
        if (p === '/system') return json(res, 200, system());
        return err(res, 404, 'not found', 'not_found');
      });
    },
  };
}
