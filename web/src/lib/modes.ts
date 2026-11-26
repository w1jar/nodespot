// The modes of the node: names, order and small helpers for the UI. Pure functions, tested in modes.test.ts.
import type { Favorite, Heard, LinkStats, ModeField, ModeInfo, NowCall, Station, Target, Vocoder } from './types';

/** The modes in the order the UI lists them. */
export const MODE_ORDER = ['allstar', 'echolink', 'bm', 'dmr', 'tgif', 'ysf', 'p25', 'nxdn', 'm17', 'echotest'] as const;

const TITLES: Record<string, string> = {
  allstar: 'AllStar', echolink: 'EchoLink', bm: 'BrandMeister', dmr: 'DMR', tgif: 'TGIF', ysf: 'YSF', p25: 'P25', nxdn: 'NXDN', m17: 'M17', echotest: 'Echo test',
};

/** The display name of a mode; the server's title wins when the list is loaded. */
export function modeTitle(name: string, list?: ModeInfo[]): string {
  return list?.find((m) => m.name === name)?.title || TITLES[name] || name.toUpperCase();
}

/** Sort modes into the fixed order; unknown names go last, alphabetically. */
export function sortModes<T extends { name: string }>(l: T[]): T[] {
  const rank = (n: string) => { const i = (MODE_ORDER as readonly string[]).indexOf(n); return i < 0 ? 99 : i; };
  return [...l].sort((a, b) => rank(a.name) - rank(b.name) || a.name.localeCompare(b.name));
}

/** "Target" label of a mode: the server's targetLabel, else a generic one. */
export const targetLabel = (m: ModeInfo | undefined): string => m?.targetLabel || 'Target';

/** What a target looks like in a line: the name when known, else its ID. */
export function targetText(t: Target | undefined): string {
  if (!t) return '';
  return t.name && t.name !== t.id ? `${t.id} ${t.name}` : t.id;
}

/** The body of POST /api/connect for a typed or picked target. */
export function targetBody(id: string, extra: Partial<Target> = {}) {
  const b: { target: string; name?: string; host?: string; port?: number; slot?: number; private?: boolean } = { target: id.trim() };
  if (extra.name) b.name = extra.name;
  if (extra.host) { b.host = extra.host; if (extra.port) b.port = extra.port; }
  if (extra.slot) b.slot = extra.slot;
  if (extra.private) b.private = true;
  return b;
}

/** Favorites of one mode, pinned first, in their saved order. */
export function favoritesOf(all: Favorite[], mode: string): Favorite[] {
  return all.filter((f) => f.mode === mode).sort((a, b) => Number(b.pinned) - Number(a.pinned) || a.position - b.position);
}

/** Recently heard targets of one mode (distinct, newest first), for the "recent" picker. */
export function recentTargets(heard: Heard[], calls: NowCall[], mode: string, limit = 8): string[] {
  const seen = new Set<string>(), out: string[] = [];
  const add = (t: string | undefined) => { const v = (t ?? '').trim(); if (v && !seen.has(v)) { seen.add(v); out.push(v); } };
  for (const c of calls) if (c.kind === mode) add(c.target);
  for (const h of heard) if (h.mode === mode) add(h.target);
  return out.slice(0, limit);
}

/** A form value draft of a mode: every field's current value; secrets start empty (blank keeps the saved one). */
export function draftOf(m: ModeInfo): Record<string, string> {
  const d: Record<string, string> = {};
  for (const f of m.fields ?? []) d[f.key] = f.secret ? '' : m.settings?.values?.[f.key] ?? f.default ?? '';
  return d;
}

/** The values to save: a blank secret is left out so the server keeps it. */
export function savePayload(fields: ModeField[], draft: Record<string, string>): Record<string, string> {
  const v: Record<string, string> = {};
  for (const f of fields) {
    const x = (draft[f.key] ?? '').trim();
    if (f.secret && x === '') continue;
    v[f.key] = f.kind === 'password' ? draft[f.key] : x;
  }
  return v;
}

/** The station values the modes fall back to. */
export type StationValues = Pick<Station, 'callsign' | 'dmrId' | 'nxdnId'>;

/** The station value a field falls back to, as the form shows it ("" when the station has none). */
export function stationValue(f: ModeField, st?: StationValues): string {
  if (!f.stationDefault || !st) return '';
  const v = f.stationDefault === 'callsign' ? (st.callsign ?? '').trim().toUpperCase() : st[f.stationDefault] ? String(st[f.stationDefault]) : '';
  return v ? v + (f.stationSuffix ?? '') : '';
}

/** The hint under a field that falls back to the station: "from station: N0CALL", or what to set when the station has none. */
export function stationHint(f: ModeField, st?: StationValues): string {
  const v = stationValue(f, st);
  if (v) return `from station: ${v}`;
  const what = f.stationDefault === 'callsign' ? 'callsign' : f.stationDefault === 'nxdnId' ? 'NXDN ID' : 'DMR ID';
  return st ? `the station has no ${what} yet` : 'empty uses the station value';
}

/** The first required field that is empty (and not a saved secret), or null. A field with a stationDefault may be empty
 * when the station has that value; `st` is the station, and leaving it out assumes the node will check. */
export function missingField(m: ModeInfo, draft: Record<string, string>, st?: StationValues): ModeField | null {
  for (const f of m.fields ?? []) {
    if (!f.required) continue;
    if (f.stationDefault && (!st || stationValue(f, st))) continue;
    if ((draft[f.key] ?? '').trim() !== '') continue;
    if (f.secret && m.secretsSet?.[f.key]) continue;
    return f;
  }
  return null;
}

/** Why a mode is not ready, in plain words, from the server's `missing` keys ("" when ready). The word "login" is only for a password. */
export function notReadyText(m: ModeInfo): string {
  if (m.configured) return '';
  const title = m.title || modeTitle(m.name);
  const out: string[] = [];
  for (const key of m.missing ?? []) {
    const f = m.fields?.find((x) => x.key === key);
    if (f?.stationDefault) {
      const what = f.stationDefault === 'callsign' ? 'callsign' : f.stationDefault === 'nxdnId' ? 'NXDN ID' : 'DMR ID';
      out.push(`Set your ${what} in Settings → Station or on this mode's page.`);
    } else if (f && (f.secret || f.kind === 'password')) out.push(`${title} needs its password.`);
    else if (f) out.push(`${title} needs its ${f.label.toLowerCase()}.`);
    else out.push(`${title} needs ${key}.`);
  }
  if (!out.length) return m.error ? `${title} cannot start: ${m.error}` : `${title} needs setting up.`;
  return out.join(' ');
}

/** A short card line for a mode that is not ready. */
export function notReadyShort(m: ModeInfo): string {
  const n = m.missing?.length ?? 0;
  return n ? `Needs ${m.missing!.map((k) => m.fields?.find((f) => f.key === k)?.label.toLowerCase() ?? k).join(', ')}` : 'Not set up';
}

/** True when the draft differs from the saved values (a typed secret counts). */
export function dirty(m: ModeInfo, draft: Record<string, string>): boolean {
  const base = draftOf(m);
  return (m.fields ?? []).some((f) => (draft[f.key] ?? '') !== (base[f.key] ?? ''));
}

/** True when the mode needs the AMBE vocoder and it is not installed and running. v is null until GET /api/vocoder answered. */
export function needsVocoder(m: ModeInfo | undefined, v: Vocoder | null): boolean {
  return !!m?.needsAmbe && !!v && !(v.installed && v.active);
}

/** Whether to keep polling GET /api/vocoder: an install is running. */
export const vocoderBusy = (v: Vocoder | null): boolean => v?.job === 'running';

/** One compact line of the AllStar link counters ("rx 120+3400 tx 0 · VNAK 0/0 · retx 0 · gaps 0"), or "" without them. */
export function linkStatsText(l: LinkStats | undefined): string {
  if (!l) return '';
  const parts = [`rx ${l.rxVoiceFull}+${l.rxVoiceMini}`, `tx ${l.txVoice}`, `VNAK ${l.vnakSent}/${l.vnakRecv}`, `retx ${l.retransmits}`];
  if (l.gaps) parts.push(`gaps ${l.gaps}`);
  if (l.dropped) parts.push(`dropped ${l.dropped}`);
  if (l.parseErrors) parts.push(`bad ${l.parseErrors}`);
  return parts.join(' · ');
}

/** A short line for a mode's link state, for the mode cards. */
export function stateText(state: string | undefined, detail?: string, err?: string): string {
  if (err) return err;
  const s = ({ idle: 'Idle', starting: 'Starting', ready: 'Ready', connecting: 'Connecting', connected: 'Connected', error: 'Error' } as Record<string, string>)[state ?? ''] ?? '';
  return detail && state !== 'idle' ? `${s} · ${detail}` : s;
}

// ---- The Modes page: mode buttons, the tune box, favorites and the dashboard pins.

/** How long a mode switch may wait for the node to report it before the page says "Not confirmed". */
export const CONFIRM_MS = 5000;
/** The longest name a favorite gets from the page (the server allows 120). */
export const FAV_NAME_MAX = 40;

/** A mode switch waiting for the node status to show it. */
export interface Pending { mode: string; since: number }
export type Outcome = 'none' | 'waiting' | 'confirmed' | 'unconfirmed';

/** `confirmed` once the node reports the mode as active, `unconfirmed` after CONFIRM_MS without it, else `waiting`. */
export function pendingOutcome(p: Pending | null, active: string | undefined, now: number): Outcome {
  if (!p) return 'none';
  if ((active ?? '') === p.mode) return 'confirmed';
  return now - p.since >= CONFIRM_MS ? 'unconfirmed' : 'waiting';
}

/** True for a mode that has a target to type (the echo test has none). */
export const hasTarget = (m: ModeInfo | undefined): boolean => !!m && m.name !== 'echotest';

/** The kind of target picker a mode gets: a search of the AllStar node list, a list of reflectors, or a plain input. */
export type PickerKind = 'nodes' | 'hosts' | 'plain' | 'none';
export function pickerKind(m: ModeInfo | undefined): PickerKind {
  if (!m || !hasTarget(m)) return 'none';
  if (m.name === 'allstar' || m.name === 'echolink') return 'nodes';
  return m.needsHost ? 'hosts' : 'plain';
}

/** The modules of an M17 reflector: A to Z. */
export const M17_MODULES: readonly string[] = Array.from({ length: 26 }, (_, i) => String.fromCharCode(65 + i));
const M17_REFLECTOR = /^M17-[A-Z0-9]{3}$/;
const M17_TARGET = /^M17-[A-Z0-9]{3} [A-Z]$/;
/** The M17 target ("M17-XXX C") from the picked or typed reflector and the module. A text that already ends in a module is taken as it is; '' when it is not an M17 designator. */
export function m17Tune(text: string, module: string): string {
  const t = text.trim().replace(/\s+/g, ' ').toUpperCase();
  if (M17_TARGET.test(t)) return t;
  return M17_REFLECTOR.test(t) ? `${t} ${module.toUpperCase()}` : '';
}
/** An M17 target split into reflector and module; null when it is not one. */
export function m17Split(value: string): { reflector: string; module: string } | null {
  const v = value.trim().toUpperCase();
  return M17_TARGET.test(v) ? { reflector: v.slice(0, 7), module: v.slice(8) } : null;
}

/** The target to connect to from what is typed: M17 adds the module, the others use the text as it is. */
export function targetValue(mode: string, text: string, module = 'A'): string {
  return mode === 'm17' ? m17Tune(text, module) : text.trim();
}

/** The placeholder of the target input. */
export function targetPlaceholder(m: ModeInfo | undefined): string {
  if (!m) return '';
  if (m.name === 'm17') return 'Reflector (M17-XXX)';
  if (m.name === 'allstar') return 'Node number, callsign or name';
  return m.targetHelp?.replace(/\.$/, '') ?? '';
}

/** A short status for a mode button: a dot tone and one word. A mode that is not ready is marked; the active one shows its link state. */
export interface Chip { tone: 'ok' | 'warn' | 'err' | 'off'; text: string }
export function modeChip(m: ModeInfo, active: string, state: string | undefined): Chip {
  if (!m.configured) return { tone: 'warn', text: 'Not set up' };
  if (m.name === active) {
    if (state === 'connected') return { tone: 'ok', text: m.name === 'echotest' ? 'Running' : 'Connected' };
    if (state === 'error') return { tone: 'err', text: 'Error' };
    if (state === 'connecting' || state === 'starting') return { tone: 'warn', text: 'Connecting' };
    return { tone: 'ok', text: 'Active' };
  }
  if (m.error) return { tone: 'err', text: 'Error' };
  return { tone: 'off', text: 'Ready' };
}

/** Favorites of one mode in the order the server keeps them (the order the page reorders). */
export const favoritesInOrder = (all: Favorite[], mode: string): Favorite[] => all.filter((f) => f.mode === mode);

/** The ids of a mode's favorites after moving one by `delta` places; null when it cannot move that way. */
export function movedIds(list: Pick<Favorite, 'id'>[], id: number, delta: number): number[] | null {
  const i = list.findIndex((f) => f.id === id), j = i + delta;
  if (i < 0 || j < 0 || j >= list.length) return null;
  const ids = list.map((f) => f.id);
  [ids[i], ids[j]] = [ids[j], ids[i]];
  return ids;
}

/** The favorite of a mode with this target, if any. */
export const findFavorite = (all: Favorite[], mode: string, target: string): Favorite | undefined => all.find((f) => f.mode === mode && f.target === target.trim());

/** Why the current target cannot be saved as a favorite ("" when it can). */
export function favoriteBlock(all: Favorite[], mode: string, target: string): string {
  const t = target.trim();
  if (!t) return 'Enter or connect a target first';
  if (findFavorite(all, mode, t)) return 'Already a favorite';
  return '';
}

/** A name a new favorite gets: the target's name when known, else its ID, cut to FAV_NAME_MAX. */
export function defaultFavName(target: string, name?: string): string {
  const n = (name && name !== target ? name : target).trim();
  return [...n].slice(0, FAV_NAME_MAX).join('');
}

/** The name of a favorite when it is valid (1 to FAV_NAME_MAX printable characters), else null. */
export function validFavName(name: string): string | null {
  const t = name.trim();
  const n = [...t].length;
  return n >= 1 && n <= FAV_NAME_MAX && ![...t].some((c) => c < ' ' || c === '\x7f') ? t : null;
}

/** The pinned favorites for the dashboard, in the server's order. */
export const pinnedFavorites = (all: Favorite[]): Favorite[] => all.filter((f) => f.pinned);

/** True when a favorite is what the node is connected to now (its mode is active and its target is the current one). */
export function favoriteActive(f: Pick<Favorite, 'mode' | 'target'>, active: string | undefined, target: Target | undefined): boolean {
  if (!target || f.mode !== active) return false;
  const a = target.id.trim().toUpperCase(), b = f.target.trim().toUpperCase();
  return a === b || (f.mode === 'm17' && b.startsWith(a + ' '));
}


/** The echo test's codec choices: the stored value and the label the picker shows. */
export const ECHO_CODECS: { value: string; label: string }[] = [
  { value: 'plain', label: 'Plain (no codec)' },
  { value: 'ambe', label: 'AMBE (DMR, YSF, NXDN)' },
  { value: 'imbe', label: 'IMBE (P25)' },
  { value: 'codec2', label: 'Codec 2 (M17)' },
  { value: 'gsm', label: 'GSM (EchoLink)' },
  { value: 'ulaw', label: 'µ-law (AllStar)' },
];

/** The echo test's saved codec ("plain" when none is stored or the stored one is unknown). */
export function echoCodec(m: ModeInfo | undefined): string {
  const c = (m?.settings?.values?.codec ?? '').trim().toLowerCase();
  return ECHO_CODECS.some((o) => o.value === c) ? c : 'plain';
}

/** The values to save for a new echo test codec. The server replaces a mode's values, so the stored ones (the playback delay) go along. */
export function echoCodecValues(m: ModeInfo | undefined, codec: string): Record<string, string> {
  return { ...(m?.settings?.values ?? {}), codec };
}
