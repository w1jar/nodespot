// In-memory backup and restore for `npm run dev:mock` (docs/BACKUP.md, /api/backup/*, /api/restore/*). Dev only.
import type { ServerResponse } from 'node:http';

type User = { username: string; role: string } | undefined;
type Ctx = {
  m: string; p: string; body: any; raw: string; res: ServerResponse; user: User; setupNeeded: boolean; setupDone: () => void;
  json: (code: number, body?: unknown) => void; err: (code: number, msg: string, c: string) => void; restart: () => void;
};

const MOCK_PASSWORD = 'pass', MOCK_CODE = '123456';
const pending = new Map<string, true>();
let lastResult: unknown = null;

const preview = () => ({
  backup: { hostname: 'node-demo', created: new Date(Date.now() - 3 * 86400e3).toISOString(), createdBy: 'admin', nodespotVersion: '0.0.1', arch: 'arm64', osId: 'debian', osCodename: 'trixie', osName: 'Debian GNU/Linux 13 (trixie)', callsign: 'N0CALL', channel: 'stable' },
  node: { arch: 'arm64', osId: 'debian', osCodename: 'trixie', osName: 'Debian GNU/Linux 13 (trixie)', callsign: 'N0CALL' },
  warnings: [{ code: 'secrets_missing', message: 'The backup has no mode passwords. Set them up again after the restore.' }],
  users: [{ username: 'admin', role: 'admin' }, { username: 'op', role: 'operator' }, { username: 'guest', role: 'viewer' }],
  favorites: 5,
  schedules: 2,
  checklist: [
    { item: 'Hostname', value: 'node-demo' }, { item: 'Timezone', value: 'America/Chicago' },
    { item: 'Network and Wi-Fi', value: 'Not included in the backup' }, { item: 'SSH keys and OS users', value: 'Not included in the backup' },
  ],
  radio: { bandwidth: 'narrow', rxMHz: 446.0, txMHz: 446.0, rxTone: '123.0', txTone: '123.0', squelch: 4 },
  parts: { node: true, sa818: true, secrets: false, conf: true },
  modes: ['allstar', 'echolink', 'bm'],
  missingSecrets: ['echolink'],
});

/** Returns true when the request was handled. Runs before the login check: the first-run page uploads with a setup code. */
export function handleBackup({ m, p, raw, res, user, setupNeeded, setupDone, body, json, err, restart }: Ctx): boolean {
  if (p === '/backup/info' && m === 'GET') {
    if (!user) return err(401, 'authentication required', 'unauthorized'), true;
    if (user.role !== 'admin') return err(403, 'forbidden', 'forbidden'), true;
    json(200, { node: true, secrets: true, sa818: true });
    return true;
  }
  if (p === '/backup' && m === 'POST') {
    if (!user) return err(401, 'authentication required', 'unauthorized'), true;
    if (user.role !== 'admin') return err(403, 'forbidden', 'forbidden'), true;
    const d = new Date(), z = (n: number) => String(n).padStart(2, '0');
    res.statusCode = 200; res.setHeader('Content-Type', 'application/gzip');
    res.setHeader('Content-Disposition', `attachment; filename="nodespot-backup-node-demo-${d.getFullYear()}${z(d.getMonth() + 1)}${z(d.getDate())}-${z(d.getHours())}${z(d.getMinutes())}.tar.gz"`);
    res.end(`mock backup (not a real archive) ${JSON.stringify(body)}\n`);
    return true;
  }
  if (p === '/restore/upload' && m === 'POST') {
    const field = (n: string) => new RegExp(`name="${n}"\\r\\n\\r\\n([^\\r]*)\\r\\n`).exec(raw)?.[1];
    const code = field('code'), pw = field('password');
    if (code !== undefined) { if (!setupNeeded || code !== MOCK_CODE) return err(403, 'wrong setup code (mock: 123456)', 'bad_setup_code'), true; }
    else if (!user || user.role !== 'admin') return err(user ? 403 : 401, 'forbidden', 'forbidden'), true;
    else if (pw !== MOCK_PASSWORD) return err(403, 'wrong password (mock: pass)', 'bad_password'), true;
    const name = /filename="([^"]*)"/.exec(raw)?.[1] ?? '';
    if (/bad/i.test(name)) return err(400, 'not a valid backup', 'bad_backup'), true;
    if (/new/i.test(name)) return err(409, 'backup is from a newer nodespot; update nodespot first', 'backup_too_new'), true;
    const id = Math.random().toString(36).slice(2, 12); pending.set(id, true);
    setTimeout(() => json(200, { id, preview: preview() }), 600);
    return true;
  }
  let mm: RegExpExecArray | null;
  if ((mm = /^\/restore\/([A-Za-z0-9]+)\/apply$/.exec(p)) && m === 'POST') {
    if (!pending.has(mm[1])) return err(404, 'no such restore', 'not_found'), true;
    if (!user && !(setupNeeded && body.code === MOCK_CODE)) return err(403, 'wrong setup code', 'bad_setup_code'), true;
    pending.delete(mm[1]);
    const steps = [
      { name: 'Stop the node', ok: true, detail: '' }, { name: 'Restore the node configuration', ok: true, detail: 'station and 3 modes' },
      { name: 'Stage nodespot database', ok: true, detail: 'restart pending' }, { name: 'Start the node', ok: true, detail: '' },
    ];
    lastResult = {
      at: new Date().toISOString(), backupCreated: preview().backup.created, steps,
      health: { ok: true, checks: [{ name: 'Node running', ok: true, detail: '' }, { name: 'Control socket answers', ok: true, detail: '' }, { name: 'Radio present', ok: true, detail: '' }] },
      checklist: preview().checklist,
    };
    json(200, { ok: true, steps, restarting: true });
    // Simulate the restart: drop streams, then refuse connections for a few seconds.
    setTimeout(() => { restart(); downUntil = Date.now() + 6000; if (setupNeeded) setupDone(); }, 800);
    return true;
  }
  if ((mm = /^\/restore\/([A-Za-z0-9]+)$/.exec(p)) && m === 'DELETE') { pending.delete(mm[1]); json(204); return true; }
  if (p === '/restore/result' && m === 'GET') {
    if (!lastResult) return err(404, 'no restore result', 'not_found'), true;
    json(200, lastResult); return true;
  }
  return false;
}

/** The plugin asks this to fake a restarting server. */
export let downUntil = 0;
