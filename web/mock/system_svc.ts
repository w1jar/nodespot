// In-memory web port of nodespot for `npm run dev:mock` (docs/API.md "Web port"). Dev only.
type Ctx = {
  m: string; p: string; url: URL; body: any; user: { username: string; role: string } | undefined;
  json: (code: number, body?: unknown) => void; err: (code: number, msg: string, c: string) => void;
};

let port = 8080;
let pending = false;

function webPort() {
  return {
    available: true, listen: ':' + port, port, on80: port === 80, canMove: port !== 80,
    reason: port === 80 ? 'already' : '', dropIn: port === 80, previous: ':8080', backPort: 8080,
    ...(pending ? { pending: { deadline: new Date(Date.now() + 60e3).toISOString(), seconds: 60 } } : {}),
  };
}

export function handleSvc(c: Ctx): boolean {
  const { m, p } = c;
  if (p !== '/system/web-port') return false;
  if (c.user?.role !== 'admin') { c.err(403, 'forbidden', 'forbidden'); return true; }
  if (m === 'GET') { c.json(200, webPort()); return true; }
  if (m === 'PUT') {
    const a = c.body?.action;
    if (a === 'move') {
      if (port === 80) { c.err(409, 'nodespot already uses port 80', 'already_port80'); return true; }
      port = 80; pending = true;
    } else if (a === 'back') {
      if (port !== 80) { c.err(409, 'nodespot is not on port 80', 'not_port80'); return true; }
      port = 8080; pending = false;
    } else if (a === 'confirm') pending = false;
    else { c.err(400, 'action must be move, back or confirm', 'bad_request'); return true; }
    c.json(200, webPort()); return true;
  }
  return false;
}
