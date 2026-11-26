// Pure routing helpers for the Settings area (#/settings/<group> and #/settings/<group>/<tab>).

/** Every flat section id of the first Settings layout (#/settings/<id>) and where it lives now: [group, tab?]. */
export const OLD_IDS: Readonly<Record<string, readonly [string, string?]>> = {
  'audio-levels': ['station', 'radio'],
  'dv-engine': ['modes'],
  'dv-identity': ['modes'],
  'dv-bridge': ['modes'],
  'dv-audio': ['modes'],
  'dv-user-list': ['modes', 'lookup'],
  'dv-callsigns': ['modes', 'lookup'],
  'users': ['access'],
  'public': ['access'],
  'overview': ['system', 'overview'],
  'updates': ['system', 'updates'],
  'network': ['system', 'network'],
  'wifi': ['system', 'wifi'],
  'web-port': ['system', 'web-port'],
  'terminal': ['system', 'terminal'],
  'backup': ['maintenance', 'backup'],
  'logs': ['maintenance', 'logs'],
  'diagnostics': ['maintenance', 'diagnostics'],
  'audit': ['maintenance', 'audit'],
  'about': ['about'],
  'account': ['account'],
};

const route = ([g, t]: readonly [string, string?]) => '/settings/' + g + (t ? '/' + t : '');

/** Old top-level routes that moved; the router rewrites them so old links and bookmarks keep working. */
export const MOVED: Readonly<Record<string, string>> = {
  '/updates': route(OLD_IDS['updates']),
  '/tune': route(OLD_IDS['audio-levels']),
  '/digital': route(OLD_IDS['dv-engine']),
  '/backup': route(OLD_IDS['backup']),
  '/system': route(OLD_IDS['overview']),
  ...Object.fromEntries(['overview', 'updates', 'network', 'wifi', 'web-port', 'terminal', 'logs', 'diagnostics', 'audit'].map((id) => ['/system/' + id, route(OLD_IDS[id])])),
  ...Object.fromEntries(Object.entries(OLD_IDS).map(([id, to]) => ['/settings/' + id, route(to)])),
};

/** The Settings -> Modes pages are gone: the settings of a mode are on the Modes page (#/modes?mode=<mode>), and the caller lookup is under Station. */
export function modesMoved(path: string): string | null {
  const m = /^\/settings\/modes(?:\/([a-z0-9-]+))?\/?$/.exec(path);
  if (!m) return null;
  if (m[1] === 'lookup') return '/settings/station/lookup';
  return m[1] ? '/modes?mode=' + m[1] : '/modes';
}

export function movedPath(path: string): string | null {
  const p = path.length > 1 ? path.replace(/\/$/, '') : path;
  const mm = modesMoved(p);
  if (mm) return mm;
  let to = Object.hasOwn(MOVED, p) ? MOVED[p] : null;
  if (to) to = modesMoved(to) ?? to;
  // The "same" route (about, account) maps to itself: nothing to rewrite.
  if (to && to !== p) return to;
  if (to) return null;
  // An unknown old System section lands on its first one, as it did before.
  return p.startsWith('/system/') ? route(OLD_IDS['overview']) : null;
}

export function isSettingsPath(path: string): boolean {
  return path === '/settings' || path.startsWith('/settings/');
}

/** A group as the router sees it: its id and its tab ids in order (empty for a single-page group). */
export interface NavGroup { id: string; tabs: readonly string[] }

export interface SettingsRoute { group: string; tab: string | null }

/**
 * The group and tab a settings route shows, or null when the page does not exist for this viewer (the app then
 * shows the Dashboard). `groups` are the ones the viewer may open; `fallback` is where "#/settings" (or an unknown
 * group, for an admin) lands. A viewer who may only open Account and About never gets the fallback for an unknown
 * group. A missing or unknown tab shows the group's first tab; a single-page group has no tab.
 */
export function settingsRoute(path: string, groups: readonly NavGroup[], fallback: string, admin: boolean): SettingsRoute | null {
  if (!isSettingsPath(path)) return null;
  const m = /^\/settings(?:\/([a-z0-9-]+)(?:\/([a-z0-9-]+))?)?\/?$/.exec(path);
  const want = m ? m[1] : '?';
  let g = groups.find((x) => x.id === want);
  if (!g) {
    if (want && !admin) return null;
    g = groups.find((x) => x.id === fallback);
    if (!g) return null;
    return { group: g.id, tab: g.tabs[0] ?? null };
  }
  const tab = m?.[2] && g.tabs.includes(m[2]) ? m[2] : (g.tabs[0] ?? null);
  return { group: g.id, tab };
}

/** Line-count clamp, the same bounds as the server (50..1000). */
export function clampLines(n: number): number {
  return Number.isFinite(n) ? Math.min(1000, Math.max(50, Math.trunc(n))) : 200;
}

/** Short summary of an audit detail JSON ({"from":"a","to":"b"}) for the table. */
export function auditSummary(detail: string): string {
  if (!detail) return '';
  try {
    const d = JSON.parse(detail) as Record<string, unknown>;
    if (d && typeof d === 'object' && !Array.isArray(d)) {
      const e = Object.entries(d);
      if (!e.length) return '';
      return e.map(([k, v]) => `${k}: ${typeof v === 'object' ? JSON.stringify(v) : String(v)}`).join(', ');
    }
  } catch { /* not JSON: show as stored */ }
  return detail;
}
