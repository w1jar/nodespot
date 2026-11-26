import type {
  About, BackupInfo, BackupSelection, Favorite, HostList, HostPatch, HostStatus, Heard, ManualAction, ModeInfo, ModeSettings, NodeJSON, NodeService,
  InputLevel, NowCalling, PublicInfo, PublicSettings, RadioInfo, RadioLevels, RestoreApplyResult, RestoreResult, RestoreUpload, Schedule, ScheduleInput, SchedulesSnapshot,
  Settings, Station, SysLogs, SysNetwork, SysServices, SystemInfo, Target, TerminalSessionInfo, TerminalStatus, TerminalTicket, UpdateInfo, User,
  UserListStatus, Vocoder, HamDBStatus, DiagPlan, AuditPage, WifiScan, WifiState, NodeInfo,
} from './types';
import type { WebPortStatus } from './svcweb';

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public body?: Record<string, unknown>) { super(message); }
}

let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void) { onUnauthorized = fn; }

async function req<T>(method: string, path: string, body?: unknown, opts: { noAuthRedirect?: boolean } = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (method !== 'GET') headers['X-nodespot'] = '1';
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  let res: Response;
  try {
    res = await fetch('/api' + path, { method, headers, credentials: 'same-origin', body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    throw new ApiError(0, 'network', 'Cannot reach the server');
  }
  if (res.status === 401 && !opts.noAuthRedirect) onUnauthorized();
  if (!res.ok) {
    let code = 'error', msg = `${res.status} ${res.statusText}`, extra: Record<string, unknown> | undefined;
    try { const j = await res.json(); if (j.error) msg = j.error; if (j.code) code = j.code; extra = j; } catch { /* not json */ }
    throw new ApiError(res.status, code, msg, extra);
  }
  if (res.status === 204 || res.status === 202 && !res.headers.get('content-type')?.includes('json')) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

/** One GET /api/session with a timeout; resolves with the HTTP status and the server instance id, rejects when unreachable. */
export async function probeServer(timeoutMs = 4000): Promise<{ status: number; instance: string }> {
  const ac = new AbortController();
  const t = setTimeout(() => ac.abort(), timeoutMs);
  try {
    const res = await fetch('/api/session', { headers: { Accept: 'application/json' }, credentials: 'same-origin', cache: 'no-store', signal: ac.signal });
    return { status: res.status, instance: res.headers.get('X-nodespot-Instance') ?? '' };
  } finally { clearTimeout(t); }
}

const q = encodeURIComponent;

/** Largest backup the server accepts (docs/BACKUP.md). */
export const MAX_BACKUP_BYTES = 256 * 1024 * 1024;

/** Saves a successful download response through an object URL, named by Content-Disposition; an error response becomes an ApiError. */
async function saveDownload(res: Response, fallback: string): Promise<string> {
  if (res.status === 401) onUnauthorized();
  if (!res.ok) {
    let code = 'error', msg = `${res.status} ${res.statusText}`;
    try { const j = await res.json(); if (j.error) msg = j.error; if (j.code) code = j.code; } catch { /* not json */ }
    throw new ApiError(res.status, code, msg);
  }
  const cd = res.headers.get('content-disposition') ?? '';
  const name = decodeURIComponent(/filename\*=UTF-8''([^;]+)/i.exec(cd)?.[1] ?? /filename="?([^";]+)"?/i.exec(cd)?.[1] ?? fallback);
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
  return name;
}

/** POST /api/backup: fetch as a blob and save it through an object URL, named by Content-Disposition. `secrets` keeps the network passwords in the file. */
async function downloadBackup(sel: BackupSelection): Promise<string> {
  let res: Response;
  try {
    res = await fetch('/api/backup', { method: 'POST', credentials: 'same-origin', headers: { 'X-nodespot': '1', 'Content-Type': 'application/json', Accept: 'application/gzip' }, body: JSON.stringify(sel) });
  } catch { throw new ApiError(0, 'network', 'Cannot reach the server'); }
  return saveDownload(res, 'nodespot-backup.tar.gz');
}

/** GET /api/system/diagnostics/bundle: the diagnostics .tar.gz, saved like a backup. */
async function downloadDiagnostics(maskIdentity: boolean): Promise<string> {
  let res: Response;
  try {
    res = await fetch(`/api/system/diagnostics/bundle?mask=${maskIdentity ? 1 : 0}`, { credentials: 'same-origin', headers: { Accept: 'application/gzip' } });
  } catch { throw new ApiError(0, 'network', 'Cannot reach the server'); }
  return saveDownload(res, 'nodespot-diagnostics.tar.gz');
}

/** POST /api/restore/upload with progress (fetch cannot report upload progress). */
function restoreUpload(file: File, auth: { password?: string; code?: string }, onProgress: (frac: number) => void): Promise<RestoreUpload> {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    fd.append('file', file, file.name);
    if (auth.password !== undefined) fd.append('password', auth.password);
    if (auth.code !== undefined) fd.append('code', auth.code);
    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/api/restore/upload');
    xhr.setRequestHeader('X-nodespot', '1');
    xhr.setRequestHeader('Accept', 'application/json');
    xhr.upload.onprogress = (e) => { if (e.lengthComputable) onProgress(e.loaded / e.total); };
    xhr.onerror = () => reject(new ApiError(0, 'network', 'Cannot reach the server'));
    xhr.onabort = () => reject(new ApiError(0, 'aborted', 'Upload canceled'));
    xhr.onload = () => {
      let j: any = null;
      try { j = JSON.parse(xhr.responseText); } catch { /* not json */ }
      if (xhr.status >= 200 && xhr.status < 300 && j) return resolve(j as RestoreUpload);
      reject(new ApiError(xhr.status, j?.code ?? 'error', j?.error ?? `${xhr.status} ${xhr.statusText}`, j ?? undefined));
    };
    xhr.send(fd);
  });
}
/** The target of a select or connect: the ID, and for hosts the address the pickers resolved. */
export type TargetBody = { target: string; name?: string; host?: string; port?: number; slot?: number; private?: boolean };

export const api = {
  setupNeeded: () => req<{ needed: boolean; hotspot?: boolean; station?: boolean }>('GET', '/setup', undefined, { noAuthRedirect: true }),
  setup: (code: string, username: string, password: string) => req<void>('POST', '/setup', { code, username, password }, { noAuthRedirect: true }),
  login: (username: string, password: string) => req<User>('POST', '/login', { username, password }, { noAuthRedirect: true }),
  logout: () => req<void>('POST', '/logout', undefined, { noAuthRedirect: true }),
  session: () => req<User>('GET', '/session', undefined, { noAuthRedirect: true }),
  password: (current: string, next: string) => req<void>('POST', '/password', { current, new: next }),

  users: () => req<User[]>('GET', '/users'),
  addUser: (username: string, password: string, role: string) => req<User>('POST', '/users', { username, password, role }),
  updateUser: (id: number, patch: { role?: string; password?: string }) => req<User>('PUT', `/users/${id}`, patch),
  deleteUser: (id: number) => req<void>('DELETE', `/users/${id}`),

  // The node: status, modes, station, the one active mode.
  node: () => req<NodeJSON>('GET', '/node'),
  nodeReload: () => req<NodeJSON>('POST', '/node/reload'),
  modes: () => req<ModeInfo[]>('GET', '/modes'),
  saveMode: (mode: string, s: ModeSettings) => req<ModeInfo[]>('PUT', `/modes/${q(mode)}`, s),
  deleteMode: (mode: string) => req<void>('DELETE', `/modes/${q(mode)}`),
  vocoder: () => req<Vocoder>('GET', '/vocoder'),
  installVocoder: () => req<Vocoder>('POST', '/vocoder/install'),
  station: () => req<Station>('GET', '/station'),
  saveStation: (s: Station) => req<NodeJSON>('PUT', '/station', s),
  /** Makes `mode` the active one (and connects `t` when given); an empty mode makes the node idle. */
  select: (mode: string, t?: TargetBody) => req<NodeJSON>('POST', '/select', { mode, ...(t ?? {}) }),
  connect: (t: TargetBody) => req<NodeJSON>('POST', '/connect', t),
  disconnect: () => req<NodeJSON>('POST', '/disconnect'),
  heard: (limit = 50) => req<Heard[]>('GET', `/heard?limit=${limit}`),
  radio: () => req<RadioLevels>('GET', '/radio'),
  setRadio: (p: { rxVolume?: number; txVolume?: number }) => req<RadioLevels>('PUT', '/radio', p),
  radioInput: () => req<InputLevel>('GET', '/radio/input'),
  radioInfo: () => req<RadioInfo>('GET', '/radio/info'),
  radioProgram: () => req<NodeJSON>('POST', '/radio/program'),
  hostLists: (mode: string) => req<HostList>('GET', `/hostlists?mode=${q(mode)}`),
  refreshHostLists: () => req<unknown>('POST', '/hostlists/refresh'),
  nodeService: () => req<NodeService>('GET', '/system/node-service'),
  restartNodeService: () => req<{ ok: boolean }>('POST', '/system/node-service/restart'),

  favorites: (mode = '') => req<Favorite[]>('GET', `/favorites${mode ? '?mode=' + q(mode) : ''}`),
  addFavorite: (f: { mode: string; target: string; label?: string; group?: string }) => req<Favorite>('POST', '/favorites', f),
  updateFavorite: (id: number, f: { target?: string; label?: string; group?: string; pinned?: boolean }) => req<Favorite>('PUT', `/favorites/${id}`, f),
  deleteFavorite: (id: number) => req<void>('DELETE', `/favorites/${id}`),
  reorder: (mode: string, ids: number[]) => req<void>('POST', '/favorites/reorder', { mode, ids }),

  schedules: () => req<SchedulesSnapshot>('GET', '/schedules'),
  addSchedule: (s: ScheduleInput) => req<Schedule>('POST', '/schedules', s),
  updateSchedule: (id: number, s: ScheduleInput) => req<Schedule>('PUT', `/schedules/${id}`, s),
  deleteSchedule: (id: number) => req<void>('DELETE', `/schedules/${id}`),
  setSchedulesEnabled: (enabled: boolean) => req<SchedulesSnapshot>('PUT', '/schedules/enabled', { enabled }),
  setScheduleSettings: (manualAction: ManualAction) => req<SchedulesSnapshot>('PUT', '/schedules/settings', { manualAction }),

  lookup: (node: string) => req<NodeInfo>('GET', `/lookup/${q(node)}`),
  search: (text: string, limit = 50) => req<NodeInfo[]>('GET', `/lookup?q=${q(text)}&limit=${limit}`),
  settings: () => req<Settings>('GET', '/settings'),
  system: () => req<SystemInfo>('GET', '/system'),
  about: () => req<About>('GET', '/about'),
  updates: () => req<UpdateInfo>('GET', '/updates'),
  checkUpdates: () => req<UpdateInfo>('POST', '/updates/check'),
  applyUpdate: () => req<UpdateInfo>('POST', '/updates/apply'),
  osCheck: () => req<UpdateInfo>('POST', '/updates/os/check'),
  osApply: () => req<UpdateInfo>('POST', '/updates/os/apply'),
  setUpdateChannel: (channel: 'stable' | 'beta') => req<UpdateInfo>('PUT', '/updates/channel', { channel }),
  reboot: () => req<{ ok: boolean }>('POST', '/system/reboot'),
  poweroff: () => req<void>('POST', '/system/poweroff'),
  restartNodespot: () => req<{ ok: boolean }>('POST', '/system/restart-nodespot'),
  sysHost: () => req<HostStatus>('GET', '/system/host'),
  setSysHost: (p: HostPatch) => req<HostStatus>('PUT', '/system/host', p),
  sysTimezones: () => req<{ timezones: string[] }>('GET', '/system/timezones'),
  sysLogUnits: () => req<{ units: string[] }>('GET', '/system/logs'),
  sysLogs: (unit: string, lines: number, since: string) => req<SysLogs>('GET', `/system/logs?unit=${q(unit)}&lines=${lines}${since ? '&since=' + q(since) : ''}`),
  sysServices: () => req<SysServices>('GET', '/system/services'),
  sysNetwork: () => req<SysNetwork>('GET', '/system/network'),
  webPort: () => req<WebPortStatus>('GET', '/system/web-port'),
  setWebPort: (action: 'move' | 'back' | 'confirm') => req<WebPortStatus>('PUT', '/system/web-port', { action }),
  wifi: () => req<WifiState>('GET', '/system/wifi'),
  wifiScan: () => req<WifiScan>('POST', '/system/wifi/scan'),
  wifiAdd: (ssid: string, password: string, hidden: boolean) => req<{ wifi: WifiState }>('POST', '/system/wifi/networks', { ssid, password, hidden }),
  wifiForget: (id: string) => req<WifiState>('DELETE', `/system/wifi/networks/${q(id)}`),
  wifiOrder: (order: string[]) => req<WifiState>('PUT', '/system/wifi/order', { order }),
  wifiHotspot: (enabled: boolean, delaySeconds = 0) => req<WifiState>('PUT', '/system/wifi/hotspot', { enabled, delaySeconds }),
  terminal: () => req<TerminalStatus>('GET', '/system/terminal'),
  setTerminal: (enabled: boolean) => req<TerminalStatus>('PUT', '/system/terminal', { enabled }),
  terminalTicket: (session?: string) => req<TerminalTicket>('POST', '/system/terminal/ticket', session ? { session } : undefined),
  terminalSessions: () => req<{ sessions: TerminalSessionInfo[] }>('GET', '/system/terminal/sessions'),
  endTerminalSession: (id: string) => req<{ ok: boolean }>('POST', `/system/terminal/sessions/${id}/end`),
  diagPlan: () => req<DiagPlan>('GET', '/system/diagnostics'),
  diagBundle: downloadDiagnostics,
  audit: (o: { before?: number; limit?: number; user?: string; action?: string } = {}) => req<AuditPage>('GET', `/audit?limit=${o.limit ?? 50}${o.before ? '&before=' + o.before : ''}${o.user ? '&user=' + q(o.user) : ''}${o.action ? '&action=' + q(o.action) : ''}`),

  nowcalling: () => req<NowCalling>('GET', '/nowcalling'),
  userList: () => req<UserListStatus>('GET', '/dv/userlist'),
  setUserList: (enabled: boolean) => req<UserListStatus>('PUT', '/dv/userlist', { enabled }),
  downloadUserList: () => req<UserListStatus>('POST', '/dv/userlist/download', {}),
  hamdb: () => req<HamDBStatus>('GET', '/dv/hamdb'),
  setHamDB: (enabled: boolean) => req<HamDBStatus>('PUT', '/dv/hamdb', { enabled }),

  publicInfo: () => req<PublicInfo>('GET', '/public/info', undefined, { noAuthRedirect: true }),
  publicSettings: () => req<PublicSettings>('GET', '/public'),
  putPublic: (p: Partial<Omit<PublicSettings, 'defaultTitle' | 'clients'>>) => req<PublicSettings>('PUT', '/public', p),

  backupInfo: () => req<BackupInfo>('GET', '/backup/info'),
  backup: downloadBackup,
  restoreUpload,
  restoreApply: (id: string, code?: string) => req<RestoreApplyResult>('POST', `/restore/${q(id)}/apply`, code ? { code } : {}, { noAuthRedirect: true }),
  restoreCancel: (id: string) => req<void>('DELETE', `/restore/${q(id)}`, undefined, { noAuthRedirect: true }),
  restoreResult: () => req<RestoreResult>('GET', '/restore/result', undefined, { noAuthRedirect: true }),
};

/** True when a request failed without an HTTP answer (the server went away mid-request, e.g. while restarting). */
export const lostConnection = (e: unknown) => !(e instanceof ApiError);
