import { handleSvc } from './system_svc.ts';
// In-memory System tab for `npm run dev:mock` (docs/API.md "System"). Dev only.
type Ctx = {
  m: string; p: string; url: URL; body: any; user: { username: string; role: string } | undefined;
  json: (code: number, body?: unknown) => void; err: (code: number, msg: string, c: string) => void;
  file: (name: string, text: string) => void;
};

const ZONES = ['America/Chicago', 'America/New_York', 'Europe/London', 'UTC'];
const UNITS = ['nodespot', 'nodespot-node', 'nodespot-helper', 'NetworkManager', 'systemd-networkd'];
const host = { hostname: 'node-demo', prettyHostname: '', os: 'Debian GNU/Linux 12 (bookworm)', hostnameAvailable: true, hostnamectlAvailable: true, hostnamectlMissing: false, timezone: 'America/Chicago', ntp: true, ntpSynchronized: true, canNtp: true, localRtc: false, timeAvailable: true, now: '', clock: '' };
const audit: { id: number; time: string; user: string; action: string; detail: string }[] = [];
function note(user: string, action: string, detail: object) { audit.push({ id: audit.length + 1, time: new Date().toISOString(), user, action, detail: JSON.stringify(detail) }); }
for (let i = 0; i < 120; i++) audit.push({ id: audit.length + 1, time: new Date(Date.now() - (120 - i) * 3600e3).toISOString(), user: i % 3 ? 'admin' : 'op', action: i % 4 ? 'favorite.add' : 'config.apply', detail: JSON.stringify({ node: '2001', n: i }) });
const term = { enabled: false };
const termStatus = () => ({ enabled: term.enabled, available: true, sessions: 0, maxSessions: 2, idleMinutes: 15, maxHours: 8 });
const DIAG_PLAN = {
  available: true, maskDefault: true, maxBytes: 12 << 20, timeLimit: 90,
  items: [
    { name: 'README.txt', desc: 'What was collected, what was redacted, and what could not be collected' },
    { name: 'report.txt', desc: 'nodespot status report: versions, the node and its active mode, updates, recent nodespot log' },
    { name: 'versions.txt', desc: 'nodespot, the node, operating system, kernel, and package versions' },
    { name: 'system.txt', desc: 'Uptime and load, memory, disk use' },
    { name: 'services.txt', desc: 'State of the nodespot, node and network services, and listening ports' },
    { name: 'network.txt', desc: 'Interfaces, addresses, default gateway, DNS servers (no WiFi passwords or saved networks)' },
    { name: 'journal/<unit>.txt', desc: 'Last 500 journal lines of each allowed unit' },
    { name: 'config/', desc: 'The node configuration and nodespot.conf, with secret values replaced by <redacted>' },
    { name: 'audit.txt', desc: 'The last 200 audit log entries' },
  ],
  excluded: ['/etc/shadow and every other password file', 'Private keys and certificates', 'The nodespot database and session tokens', 'Saved WiFi networks'],
  redacted: ['Mode passwords (AllStar, EchoLink, DMR masters)', 'nodespot secrets', 'WiFi pre-shared keys', 'Your callsign and public IP addresses (when masking is on)'],
};
const status = () => { const d = new Date(); return { ...host, now: d.toISOString(), clock: d.toString() }; };

export function handleSystem(c: Ctx): boolean {
  const { m, p } = c;
  if (handleSvc(c)) return true;
  if (!p.startsWith('/system/') && p !== '/audit') return false;
  const known = ['/system/diagnostics', '/system/diagnostics/bundle', '/system/host', '/system/timezones', '/system/poweroff', '/system/logs', '/system/services', '/system/network', '/system/terminal', '/system/terminal/ticket', '/system/terminal/sessions', '/audit'];
  if (!known.includes(p) && !/^\/system\/terminal\/sessions\/[0-9a-f]{16}\/end$/.test(p)) return false;
  if (c.user?.role !== 'admin') { c.err(403, 'forbidden', 'forbidden'); return true; }
  const who = c.user.username;
  if (p === '/system/host' && m === 'GET') { c.json(200, status()); return true; }
  if (p === '/system/host' && m === 'PUT') {
    const b = c.body ?? {};
    if (b.hostname !== undefined) {
      if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/i.test(String(b.hostname))) { c.err(400, 'invalid hostname', 'bad_request'); return true; }
      const to = String(b.hostname).toLowerCase(); if (to !== host.hostname) note(who, 'system.hostname', { from: host.hostname, to }); host.hostname = to;
    }
    if (b.timezone !== undefined) {
      if (!ZONES.includes(b.timezone)) { c.err(400, 'unknown time zone', 'bad_request'); return true; }
      if (b.timezone !== host.timezone) note(who, 'system.timezone', { from: host.timezone, to: b.timezone }); host.timezone = b.timezone;
    }
    if (typeof b.ntp === 'boolean') { if (b.ntp !== host.ntp) note(who, 'system.ntp', { from: host.ntp, to: b.ntp }); host.ntp = b.ntp; host.ntpSynchronized = b.ntp; }
    c.json(200, status()); return true;
  }
  if (p === '/system/terminal' && m === 'GET') { c.json(200, termStatus()); return true; }
  if (p === '/system/terminal' && m === 'PUT') {
    if (typeof c.body?.enabled !== 'boolean') { c.err(400, 'nothing to change', 'bad_request'); return true; }
    if (c.body.enabled !== term.enabled) note(who, 'system.terminal', { from: term.enabled, to: c.body.enabled }); term.enabled = c.body.enabled;
    c.json(200, termStatus()); return true;
  }
  if (p === '/system/terminal/sessions' && m === 'GET') { c.json(200, { sessions: [] }); return true; }
  if (p.startsWith('/system/terminal/sessions/') && m === 'POST') { c.err(404, 'no such session', 'not_found'); return true; }
  if (p === '/system/terminal/ticket' && m === 'POST') {
    if (!term.enabled) { c.err(403, 'the terminal is turned off', 'terminal_disabled'); return true; }
    c.json(200, { ticket: 'mock', expiresIn: 30 }); return true;
  }
  if (p === '/system/diagnostics') { c.json(200, DIAG_PLAN); return true; }
  if (p === '/system/diagnostics/bundle') {
    const mask = c.url.searchParams.get('mask') ?? '1';
    if (!['0', '1'].includes(mask)) { c.err(400, 'mask must be 0 or 1', 'bad_request'); return true; }
    note(who, 'system.diagnostics.download', { maskIdentity: mask === '1' });
    const d = new Date(), z = (n: number) => String(n).padStart(2, '0');
    c.file(`nodespot-diagnostics-${d.getFullYear()}${z(d.getMonth() + 1)}${z(d.getDate())}-${z(d.getHours())}${z(d.getMinutes())}.tar.gz`, `mock diagnostics bundle (not a real archive), identity ${mask === '1' ? 'masked' : 'not masked'}\n`);
    return true;
  }
  if (p === '/system/timezones') { c.json(200, { timezones: ZONES }); return true; }
  if (p === '/system/poweroff' && m === 'POST') { note(who, 'system.poweroff', {}); c.json(202, { ok: true }); return true; }
  if (p === '/system/logs') {
    const unit = c.url.searchParams.get('unit') ?? '';
    if (!unit) { c.json(200, { units: UNITS }); return true; }
    if (!UNITS.includes(unit)) { c.err(400, 'unknown unit', 'bad_request'); return true; }
    const lines = Math.min(1000, Math.max(50, +(c.url.searchParams.get('lines') ?? 200) || 200));
    const text = Array.from({ length: Math.min(lines, 80) }, (_, i) => `${new Date(Date.now() - (80 - i) * 60e3).toISOString().slice(0, 19)}+0000 node-demo ${unit}[612]: sample log line ${i + 1}`).join('\n') + '\n';
    c.json(200, { unit, lines, since: c.url.searchParams.get('since') ?? '', text, truncated: false, available: true }); return true;
  }
  if (p === '/system/services') {
    c.json(200, { available: true, services: [['nodespot-node.service', 'nodespot node', 'active', 'running'], ['nodespot.service', 'ASL Control Panel', 'active', 'running'], ['nodespot-helper.service', 'nodespot helper', 'active', 'running'], ['NetworkManager.service', 'Network Manager', 'active', 'running'], ['chrony.service', 'chrony', 'inactive', 'dead']].map(([unit, description, active, sub]) => ({ unit, description, active, sub, enabled: 'enabled', since: active === 'active' ? 'Mon 2026-11-16 08:00:01 UTC' : '' })) }); return true;
  }
  if (p === '/system/network') {
    c.json(200, { available: true, interfaces: [
      { name: 'lo', state: 'UNKNOWN', kind: 'loopback', mac: '', mtu: 65536, addrs: [{ family: 'inet', addr: '127.0.0.1', prefix: 8, scope: 'host', dynamic: false }] },
      { name: 'eth0', state: 'UP', kind: 'ether', mac: '02:00:00:00:20:01', mtu: 1500, addrs: [{ family: 'inet', addr: '192.0.2.10', prefix: 24, scope: 'global', dynamic: true }] },
    ], gateways: [{ family: 'inet', gateway: '192.0.2.1', dev: 'eth0', metric: 100 }], dns: ['192.0.2.1'], dnsSource: 'resolv.conf' }); return true;
  }
  if (p === '/audit') {
    const q = c.url.searchParams;
    const limit = Math.min(200, Math.max(1, +(q.get('limit') ?? 50) || 50)), before = +(q.get('before') ?? 0), u = q.get('user') ?? '', a = q.get('action') ?? '';
    const all = audit.filter((r) => (!before || r.id < before) && (!u || r.user === u) && (!a || r.action.startsWith(a))).sort((x, y) => y.id - x.id);
    const page = all.slice(0, limit);
    c.json(200, { entries: page.map((r) => ({ ...r })), next: all.length > limit ? page[page.length - 1].id : null }); return true;
  }
  return false;
}
