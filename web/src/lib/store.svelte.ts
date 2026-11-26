import { api, probeServer, setUnauthorizedHandler } from './api';
import type { NowCalling, ManualAction, SchedulesSnapshot, UpdatesBadge, Favorite, Heard, ModeInfo, NodeJSON, NodeEvent, SystemInfo, User } from './types';
import { navigate } from './router.svelte';
import { applySettings } from './settings.svelte';
import { toast, toastError } from './toast.svelte';
import type { Settings } from './types';
import { RELOADED_KEY, shouldReloadForVersion } from './reload';

export const app = $state<{
  ready: boolean; setupNeeded: boolean; user: User | null;
  /** Id of the server process (SSE hello); changes when the panel restarts. */
  instance: string;
  node: NodeJSON | null; modes: ModeInfo[]; heard: Heard[]; events: NodeEvent[];
  favorites: Favorite[]; schedules: SchedulesSnapshot | null; nowcalling: NowCalling | null; system: SystemInfo | null;
  conn: 'connecting' | 'live' | 'reconnecting' | 'offline'; stateAt: number; version: string; haveFavorites: boolean;
  /** Set while an nodespot update is installing; cleared when the server returns with a new version. */
  updating: { from: string; target: string } | null;
  /** Admins only: what the header badge and the Updates tab show (SSE "updates"). */
  updates: UpdatesBadge;
  /** Set while the panel or the node restarts: the page waits for the server and reloads. */
  restarting: { kind: 'nodespot' | 'reboot'; since: number } | null;
  /** The public status view is on (admin setting); the app is the chrome-less /embed view. */
  publicEnabled: boolean; embed: boolean;
  /** This page was opened over the setup hotspot (GET /api/setup): a login typed here is not encrypted. */
  hotspot: boolean;
}>({
  ready: false, setupNeeded: false, user: null, instance: '', node: null, modes: [], heard: [], events: [], favorites: [], schedules: null, nowcalling: null, system: null,
  conn: 'offline', stateAt: Date.now(), version: '', haveFavorites: false, updating: null,
  updates: { nodespot: false, nodespotVersion: '', os: 0, security: 0, reboot: false }, restarting: null,
  publicEnabled: false, hotspot: false, embed: location.pathname === '/embed' || location.pathname === '/embed/',
});

/** Ticking clock (ms) so durations advance between updates. */
export const clock = $state<{ now: number }>({ now: Date.now() });
setInterval(() => { clock.now = Date.now(); }, 1000);

export const canOperate = () => app.user?.role === 'admin' || app.user?.role === 'operator';
export const isAdmin = () => app.user?.role === 'admin';

let es: EventSource | null = null;
let retry = 0;
let retryTimer: ReturnType<typeof setTimeout> | undefined;

function setNode(n: NodeJSON) {
  app.node = n; app.stateAt = Date.now();
}

/** Re-read the Last heard rows (after a talker ends) at most once a second. */
let heardTimer: ReturnType<typeof setTimeout> | undefined;
export function refreshHeard(now = false) {
  clearTimeout(heardTimer);
  const go = () => { api.heard(50).then((h) => { app.heard = h ?? []; }).catch(() => {}); };
  if (now) go(); else heardTimer = setTimeout(go, 800);
}
/** The mode list with settings (admins) or status only (others). */
export async function refreshModes() { app.modes = (await api.modes()) ?? []; }
export async function refreshNode() { setNode(await api.node()); }

/**
 * The panel or the node restarts. Poll the session until the server answers
 * with a different instance id (or answers again after having been down), then
 * reload (this also loads a new UI after an upgrade). A reboot is retried for
 * as long as it takes; a panel restart gives up after a minute.
 */
/**
 * `before` is the server instance captured BEFORE the restart request was sent:
 * the panel can restart (and the event stream reconnect with the new instance)
 * before that request even returns.
 */
export function awaitRestart(kind: 'nodespot' | 'reboot', before: string = app.instance) {
  if (app.restarting) return;
  const since = Date.now();
  app.restarting = { kind, since };
  restartFrom = before;
  let sawDown = false;
  const tick = async () => {
    if (!app.restarting) return;
    try {
      const r = await probeServer(4000);
      if (r.status === 401) { location.reload(); return; }
      if (sawDown || (r.instance && before && r.instance !== before)) { location.reload(); return; }
    } catch {
      sawDown = true;
    }
    if (kind === 'nodespot' && Date.now() - since > 60000) {
      app.restarting = null; restartFrom = '';
      toastError(new Error('nodespot did not restart. Check `journalctl -u nodespot`.'));
      return;
    }
    setTimeout(tick, 2000);
  };
  setTimeout(tick, 1000);
}
let restartFrom = '';

export function startEvents() {
  stopEvents();
  app.conn = retry === 0 ? 'connecting' : 'reconnecting';
  es = new EventSource('/api/events');
  const parse = <T>(e: Event) => JSON.parse((e as MessageEvent).data) as T;
  es.addEventListener('hello', (e) => {
    retry = 0; app.conn = 'live';
    const h = parse<{ user: User; version: string; instance?: string; settings?: Settings }>(e);
    if (app.restarting && h.instance && restartFrom && h.instance !== restartFrom) { location.reload(); return; }
    if (h.instance) app.instance = h.instance;
    if (app.updating && h.version && h.version !== app.updating.from) {
      // The server came back running the new version: reload to get the new UI.
      try { sessionStorage.setItem('nodespot-updated', h.version); } catch { /* ignore */ }
      location.reload();
      return;
    }
    let reloadedFor: string | null = null;
    try { reloadedFor = sessionStorage.getItem(RELOADED_KEY); } catch { /* ignore */ }
    if (shouldReloadForVersion(app.version, h.version, reloadedFor)) {
      // Installed outside the panel (apt, dpkg): this tab runs the old UI, so reload once for this version.
      try { sessionStorage.setItem(RELOADED_KEY, h.version); sessionStorage.setItem('nodespot-updated', h.version); } catch { /* ignore */ }
      location.reload();
      return;
    }
    app.version = h.version; if (h.user) app.user = h.user;
    if (h.settings) applySettings(h.settings);
  });
  es.addEventListener('node', (e) => {
    app.conn = 'live';
    const before = app.node?.active;
    setNode(parse<NodeJSON>(e));
    if (before !== app.node?.active || !app.modes.length) void refreshModes().catch(() => {});
  });
  es.addEventListener('nodeevent', (e) => {
    const ev = parse<NodeEvent>(e);
    app.events = [ev, ...app.events].slice(0, 50);
    if (ev.kind === 'talk_end') refreshHeard();
  });
  es.addEventListener('updates', (e) => { app.updates = parse<UpdatesBadge>(e); });
  es.addEventListener('system', (e) => { app.system = parse<SystemInfo>(e); });
  es.addEventListener('favorites', (e) => { app.favorites = parse<Favorite[]>(e); app.haveFavorites = true; });
  es.addEventListener('schedules', (e) => { app.schedules = parse<SchedulesSnapshot>(e); });
  es.addEventListener('nowcalling', (e) => { app.nowcalling = parse<NowCalling>(e); });
  es.onerror = () => {
    es?.close(); es = null;
    app.conn = 'reconnecting';
    // If the session expired the stream fails with 401; check before looping.
    api.session().then(() => {
      const delay = Math.min(15000, 1000 * 2 ** retry++) + Math.random() * 400;
      retryTimer = setTimeout(startEvents, delay);
    }).catch((err) => {
      if (err?.status === 401) { app.user = null; stopEvents(); navigate('/login'); }
      else { const delay = Math.min(15000, 1000 * 2 ** retry++); retryTimer = setTimeout(startEvents, delay); }
    });
  };
}
export function stopEvents() {
  clearTimeout(retryTimer);
  es?.close(); es = null; app.conn = 'offline';
}

export async function refreshSchedules() { app.schedules = await api.schedules(); }
/** The global switch (operators). The response is the new snapshot. */
export async function setSchedulesEnabled(on: boolean) { app.schedules = await api.setSchedulesEnabled(on); }
/** What a manual connect/disconnect does to the schedules (operators). */
export async function setManualAction(m: ManualAction) { app.schedules = await api.setScheduleSettings(m); }

export async function refreshFavorites() { app.favorites = (await api.favorites()) ?? []; app.haveFavorites = true; }

export async function logout() {
  try { await api.logout(); } catch { /* ignore */ }
  stopEvents(); app.user = null; app.node = null; app.modes = []; app.heard = []; app.events = []; app.favorites = []; app.schedules = null; app.nowcalling = null; app.haveFavorites = false;
  navigate('/login');
}

export function onLoggedIn(u: User) {
  app.user = u; retry = 0; startEvents();
  api.favorites().then((f) => { app.favorites = f ?? []; app.haveFavorites = true; }).catch(() => {});
  api.schedules().then((s) => { app.schedules = s; }).catch(() => {});
  api.nowcalling().then((n) => { app.nowcalling = n; }).catch(() => {});
  api.node().then(setNode).catch(() => {});
  refreshModes().catch(() => {});
  refreshHeard(true);
  api.settings().then(applySettings).catch(() => {});
  api.system().then((s) => { app.system = s; }).catch(() => {});
}

/** Learn whether the public view is on (it may be, for visitors without a session). */
export async function refreshPublicInfo(): Promise<boolean> {
  try { const i = await api.publicInfo(); app.publicEnabled = i.enabled; return i.loggedIn; } catch { app.publicEnabled = false; return true; }
}

export async function boot() {
  if (app.embed) { await refreshPublicInfo(); app.ready = true; return; }
  setUnauthorizedHandler(() => { if (app.user) { app.user = null; stopEvents(); } navigate('/login'); });
  try {
    const s = await api.setupNeeded();
    app.setupNeeded = s.needed;
    app.hotspot = !!s.hotspot;
    if (s.needed) { navigate('/setup'); app.ready = true; return; }
  } catch { /* backend down: fall through to login */ }
  // The info call says whether a session exists, so visitors never trigger a 401.
  if (await refreshPublicInfo()) {
    try { onLoggedIn(await api.session()); } catch { if (!app.publicEnabled) navigate('/login'); }
  } else if (!app.publicEnabled) navigate('/login');
  app.ready = true;
  try {
    const v = sessionStorage.getItem('nodespot-updated');
    if (v) { sessionStorage.removeItem('nodespot-updated'); toast(`Updated to ${v}`); }
  } catch { /* ignore */ }
}

/** The first-run station setup is open: an administrator is signed in and the node has no callsign yet. */
export const stationSetupOpen = () => isAdmin() && !!app.node?.setupNeeded;
