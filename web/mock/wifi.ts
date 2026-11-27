// In-memory WiFi manager for `npm run dev:mock` (docs/API.md "WiFi"). Dev only.
type Ctx = {
  m: string; p: string; url: URL; body: any; user: { username: string; role: string } | undefined;
  json: (code: number, body?: unknown) => void; err: (code: number, msg: string, c: string) => void;
};

type Net = { id: string; name: string; ssid: string; priority: number; autoconnect: boolean; active: boolean; hidden: boolean; secured: boolean };
const nets: Net[] = [
  { id: '00000000-0000-4000-8000-0000000000a1', name: 'ExampleNet', ssid: 'ExampleNet', priority: 100, autoconnect: true, active: true, hidden: false, secured: true },
  { id: '00000000-0000-4000-8000-0000000000a2', name: 'ExampleGuest', ssid: 'ExampleGuest', priority: 90, autoconnect: true, active: false, hidden: false, secured: true },
];
const hotspot = { enabled: true, delaySeconds: 180, active: false, ssid: 'nodespot-node-demo', address: '10.42.0.1', firewall: 'firewalld' };
let seq = 3;

const view = () => ({
  available: true, hasWifi: true, radio: 'enabled',
  devices: [
    { name: 'eth0', type: 'ethernet', state: 'connected', connection: 'Wired connection 1', addrs: [] },
    { name: 'wlan0', type: 'wifi', state: 'connected', connection: 'ExampleNet', addrs: ['192.0.2.20'] },
  ],
  active: 'ExampleNet', ethernet: true, hotspot: { ...hotspot },
  networks: [...nets].sort((a, b) => b.priority - a.priority), viaWifi: false, viaHotspot: false,
});

const ssidBytes = (s: string) => new TextEncoder().encode(s).length;
const pskOk = (s: string) => s === '' || (s.length === 64 ? /^[0-9a-fA-F]{64}$/.test(s) : s.length >= 8 && s.length <= 63 && /^[\x20-\x7e]+$/.test(s));

export function handleWifi(c: Ctx): boolean {
  const { m, p } = c;
  if (!p.startsWith('/system/wifi')) return false;
  if (c.user?.role !== 'admin') { c.err(403, 'forbidden', 'forbidden'); return true; }
  if (p === '/system/wifi' && m === 'GET') { c.json(200, view()); return true; }
  if (p === '/system/wifi/scan' && m === 'POST') {
    c.json(200, { networks: [
      { ssid: 'ExampleNet', signal: 82, channel: 6, security: 'WPA2', secured: true, supported: true, inUse: true, saved: true },
      { ssid: 'ExampleGuest', signal: 64, channel: 11, security: 'WPA2', secured: true, supported: true, inUse: false, saved: true },
      { ssid: 'ExampleOpen', signal: 40, channel: 1, security: '', secured: false, supported: true, inUse: false, saved: false },
      { ssid: 'ExampleCorp', signal: 33, channel: 44, security: 'WPA2 802.1X', secured: true, supported: false, inUse: false, saved: false },
    ] }); return true;
  }
  if (p === '/system/wifi/networks' && m === 'POST') {
    const b = c.body ?? {}, ssid = String(b.ssid ?? ''), pw = String(b.password ?? '');
    if (ssidBytes(ssid) < 1 || ssidBytes(ssid) > 32) { c.err(400, 'invalid SSID: 1 to 32 bytes', 'bad_request'); return true; }
    if (!pskOk(pw)) { c.err(400, 'invalid password: 8 to 63 characters (or 64 hexadecimal digits)', 'bad_request'); return true; }
    if (pw === 'wrong-pass') { c.err(502, 'could not connect: the password may be wrong', 'helper_error'); return true; }
    const old = nets.find((n) => n.ssid === ssid);
    if (old) { old.secured = pw !== ''; old.hidden = !!b.hidden; }
    else nets.push({ id: `00000000-0000-4000-8000-0000000000${(seq++).toString(16).padStart(2, '0')}`, name: ssid, ssid, priority: Math.min(...nets.map((n) => n.priority), 100) - 10, autoconnect: true, active: false, hidden: !!b.hidden, secured: pw !== '' });
    c.json(200, { result: { ssid, updated: !!old }, wifi: view() }); return true;
  }
  const del = /^\/system\/wifi\/networks\/([0-9a-f-]+)$/.exec(p);
  if (del && m === 'DELETE') {
    const i = nets.findIndex((n) => n.id === del[1]);
    if (i < 0) { c.err(502, 'unknown network', 'helper_error'); return true; }
    nets.splice(i, 1); c.json(200, view()); return true;
  }
  if (p === '/system/wifi/order' && m === 'PUT') {
    const order: string[] = Array.isArray(c.body?.order) ? c.body.order : [];
    if (order.length !== nets.length || new Set(order).size !== order.length || order.some((id) => !nets.find((n) => n.id === id))) { c.err(400, 'invalid order', 'bad_request'); return true; }
    order.forEach((id, i) => { nets.find((n) => n.id === id)!.priority = 100 - 10 * i; });
    c.json(200, view()); return true;
  }
  if (p === '/system/wifi/hotspot' && m === 'PUT') {
    const b = c.body ?? {};
    if (typeof b.enabled !== 'boolean') { c.err(400, 'enabled is required', 'bad_request'); return true; }
    if (b.delaySeconds && (b.delaySeconds < 30 || b.delaySeconds > 3600)) { c.err(400, 'invalid delay: 30 to 3600 seconds', 'bad_request'); return true; }
    hotspot.enabled = b.enabled; if (b.delaySeconds) hotspot.delaySeconds = b.delaySeconds;
    c.json(200, view()); return true;
  }
  return false;
}
