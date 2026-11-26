// Types and pure helpers of the Settings "Web port" section. No imports from the app,
// so node:test runs it directly.

export interface WebPortStatus {
  available: boolean; listen: string; port: number; on80: boolean; canMove: boolean; reason: string;
  dropIn: boolean; previous: string; backPort: number; pending?: { deadline: string; seconds: number }; reverted?: string; firewall?: string;
}

/** Address of nodespot on `port`, built from the browser's own host (plan 7.4). Port 80 is left out. */
export function webAddress(loc: { protocol: string; hostname: string }, port: number, path = '/'): string {
  const host = loc.hostname.includes(':') && !loc.hostname.startsWith('[') ? `[${loc.hostname}]` : loc.hostname;
  return `${loc.protocol}//${host}${port === 80 ? '' : ':' + port}${path}`;
}

/** Port the browser page is on (80 when the URL has none). */
export function pagePort(loc: { port: string; protocol: string }): number {
  if (loc.port) return Number(loc.port);
  return loc.protocol === 'https:' ? 443 : 80;
}

/** True when a pending move should be confirmed from this page: the page itself arrived on the new port. */
export function shouldConfirm(st: WebPortStatus, loc: { port: string; protocol: string }): boolean {
  return !!st.pending && st.on80 && pagePort(loc) === 80;
}

/**
 * After a move the old address goes away. This watches it (same origin, so the
 * page's own CSP allows it): `probe` throws while the server is down. When it
 * answers again after having been down, the helper has moved nodespot back
 * ("returned"). When it stays down until `timeoutMs`, the move stuck ("gone").
 * The new address cannot be probed from this page: the CSP only allows the
 * page's own origin, and another port is another origin.
 */
export async function watchOldAddress(o: { probe: () => Promise<unknown>; timeoutMs: number; intervalMs?: number; sleep?: (ms: number) => Promise<void>; now?: () => number }): Promise<'returned' | 'gone'> {
  const sleep = o.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  const now = o.now ?? Date.now;
  const end = now() + o.timeoutMs;
  let sawDown = false;
  for (;;) {
    try {
      await o.probe();
      if (sawDown) return 'returned';
    } catch { sawDown = true; }
    if (now() >= end) return 'gone';
    await sleep(o.intervalMs ?? 2000);
  }
}
