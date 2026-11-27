// The AMBE vocoder install for `npm run dev:mock`: GET /api/vocoder and POST /api/vocoder/install. Dev only.
type Ctx = {
  extra?: () => string[]; m: string; p: string; user: { username: string; role: string } | undefined;
  json: (code: number, body?: unknown) => void; err: (code: number, msg: string, c: string) => void;
};

/** The modes whose network carries AMBE voice. */
export const AMBE_MODES = ['bm', 'dmr', 'tgif', 'ysf', 'nxdn'];

let installed = false;
let job: '' | 'running' | 'done' = '';
let doneAt = 0;
let startedAt = '';

function status(c: { extra?: () => string[] }) {
  if (job === 'running' && Date.now() >= doneAt) { job = 'done'; installed = true; }
  return { installed, active: installed, needed: [...AMBE_MODES, ...(c.extra?.() ?? [])], supported: true, ...(job ? { job } : {}), ...(job === 'running' ? { jobStarted: startedAt, jobLog: 'Get:1 http://192.0.2.1/debian stable InRelease' } : {}) };
}

export function handleVocoder(c: Ctx): boolean {
  const { m, p } = c;
  if (p !== '/vocoder' && p !== '/vocoder/install') return false;
  if (c.user?.role !== 'admin') { c.err(403, 'forbidden', 'forbidden'); return true; }
  if (p === '/vocoder' && m === 'GET') { c.json(200, status(c)); return true; }
  if (p === '/vocoder/install' && m === 'POST') {
    if (installed) { c.json(200, status(c)); return true; }
    if (job !== 'running') { job = 'running'; startedAt = new Date().toISOString(); doneAt = Date.now() + 3000; }
    c.json(202, status(c));
    return true;
  }
  return false;
}
