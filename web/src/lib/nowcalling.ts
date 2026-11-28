// Text helpers for the Now Calling strip and its details sheet. Pure functions, tested in nowcalling.test.ts.
import type { NowCall, NowCalling } from './types';
import { modeTitle } from './modes.ts';

const US: Record<string, string> = {
  alabama: 'AL', alaska: 'AK', arizona: 'AZ', arkansas: 'AR', california: 'CA', colorado: 'CO', connecticut: 'CT', delaware: 'DE',
  'district of columbia': 'DC', florida: 'FL', georgia: 'GA', hawaii: 'HI', idaho: 'ID', illinois: 'IL', indiana: 'IN', iowa: 'IA',
  kansas: 'KS', kentucky: 'KY', louisiana: 'LA', maine: 'ME', maryland: 'MD', massachusetts: 'MA', michigan: 'MI', minnesota: 'MN',
  mississippi: 'MS', missouri: 'MO', montana: 'MT', nebraska: 'NE', nevada: 'NV', 'new hampshire': 'NH', 'new jersey': 'NJ',
  'new mexico': 'NM', 'new york': 'NY', 'north carolina': 'NC', 'north dakota': 'ND', ohio: 'OH', oklahoma: 'OK', oregon: 'OR',
  pennsylvania: 'PA', 'rhode island': 'RI', 'south carolina': 'SC', 'south dakota': 'SD', tennessee: 'TN', texas: 'TX', utah: 'UT',
  vermont: 'VT', virginia: 'VA', washington: 'WA', 'west virginia': 'WV', wisconsin: 'WI', wyoming: 'WY',
};
const US_NAMES = new Set(['united states', 'united states of america', 'usa', 'us']);

/** "Texas" -> "TX"; anything else is returned as it came. */
export function stateAbbrev(state: string | undefined): string {
  const s = (state ?? '').trim();
  return US[s.toLowerCase()] ?? s;
}

/** "City, ST" for the US, "City, Country" elsewhere; whatever is known otherwise. */
export function placeText(c: Pick<NowCall, 'city' | 'state' | 'country' | 'location'>): string {
  const city = (c.city ?? '').trim(), country = (c.country ?? '').trim();
  const us = country === '' || US_NAMES.has(country.toLowerCase());
  const region = us && (c.state ?? '').trim() ? stateAbbrev(c.state) : country;
  const out = [city, region].filter(Boolean).join(', ');
  return out || (c.location ?? '').trim();
}

/** The full place for the details sheet: city, state, country. */
export function placeFull(c: Pick<NowCall, 'city' | 'state' | 'country'>): string {
  return [c.city, c.state, c.country].map((x) => (x ?? '').trim()).filter(Boolean).join(', ');
}

/** The talker's name for the strip: the first name, else the full name's first word. */
export function firstName(c: Pick<NowCall, 'first' | 'name'>): string {
  return (c.first ?? '').trim() || (c.name ?? '').trim().split(/\s+/)[0] || '';
}

/** Who is talking: callsign, or the DMR ID when no callsign is known. */
export function who(c: Pick<NowCall, 'call' | 'dmrId' | 'node'>): string {
  return (c.call ?? '').trim() || (c.dmrId ? `ID ${c.dmrId}` : c.node ? `Node ${c.node}` : 'Unknown');
}

/** "N0CALL · Alex · Springfield, IL" with the empty parts left out. AllStar: "N0CALL · description · location" (the node list, no person). */
export function mainLine(c: NowCall): string {
  if (c.kind === 'allstar') return [who(c), (c.desc ?? '').trim(), (c.location ?? '').trim()].filter(Boolean).join(' · ');
  return [who(c), firstName(c), placeText(c)].filter(Boolean).join(' · ');
}

/** The target of a call as the UI shows it: "TG 91", "Room", "2001". */
function targetPart(c: NowCall): string {
  const t = (c.target ?? '').trim();
  if (!t) return '';
  if (c.kind === 'bm' || c.kind === 'dmr' || c.kind === 'tgif') return `TG ${t}`;
  return t;
}

/** The "via RF" tag of a call that came in over the radio; '' for a network call. */
export const viaTag = (via: string | undefined): string => (via === 'rf' ? 'via RF' : '');

/** "AllStar", "BrandMeister · TG 91", "YSF · Room". The target name is left to sourceDetail. A call heard on the hat adds "via RF". */
export function sourceLabel(c: NowCall): string {
  return [modeTitle(c.kind), targetPart(c), viaTag(c.via)].filter(Boolean).join(' · ');
}

/** The full source for the details sheet: "AllStar · 2001 Example hub", "BrandMeister · TG 91 Example". */
export function sourceDetail(c: NowCall): string {
  return [modeTitle(c.kind), [targetPart(c), (c.targetName ?? '').trim()].filter(Boolean).join(' '), viaTag(c.via)].filter(Boolean).join(' · ');
}

/** The short form for the idle line: "AllStar", "BrandMeister TG 91". */
export function sourceShort(c: NowCall): string {
  return [[modeTitle(c.kind), targetPart(c)].filter(Boolean).join(' '), viaTag(c.via)].filter(Boolean).join(' ');
}

export function qrzUrl(call: string | undefined): string {
  const c = (call ?? '').trim();
  return /^[A-Za-z0-9/]{3,12}$/.test(c) ? `https://www.qrz.com/db/${encodeURIComponent(c.toUpperCase())}` : '';
}
export function radioidUrl(c: Pick<NowCall, 'kind' | 'dmrId'>): string {
  const id = (c.dmrId ?? '').trim();
  return c.kind !== 'allstar' && c.kind !== 'echolink' && /^\d{1,9}$/.test(id) ? `https://radioid.net/database/view?id=${id}` : '';
}

/** "just now", "12 s ago", "5 min ago", "2 h ago", "3 d ago". */
export function agoText(sec: number): string {
  sec = Math.max(0, Math.floor(sec));
  if (sec < 5) return 'just now';
  if (sec < 60) return `${sec} s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)} min ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} h ago`;
  return `${Math.floor(sec / 86400)} d ago`;
}

/** Seconds -> "m:ss" (minutes keep growing past 59; "h:mm:ss" from one hour). */
export function mmss(sec: number): string {
  sec = Math.max(0, Math.floor(sec));
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = sec % 60;
  const p = (n: number) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${p(m)}:${p(s)}` : `${p(m)}:${p(s)}`;
}

/** Seconds a call lasted: its end (or `now` while talking) minus its start; null when unknown. */
export function durationSec(c: NowCall, now: number): number | null {
  const s = Date.parse(c.start);
  if (!Number.isFinite(s)) return null;
  if (c.active) return Math.max(0, (now - s) / 1000);
  const e = c.end ? Date.parse(c.end) : NaN;
  return Number.isFinite(e) ? Math.max(0, (e - s) / 1000) : null;
}

/** "12 s", "1 min 5 s" for a finished call. */
export function durText(sec: number | null): string {
  if (sec == null) return '';
  sec = Math.round(sec);
  if (sec < 60) return `${sec} s`;
  const m = Math.floor(sec / 60), s = sec % 60;
  return s ? `${m} min ${s} s` : `${m} min`;
}

/** Seconds since a finished call ended (its start when the end is unknown). */
export function sinceSec(c: NowCall, now: number): number {
  const t = Date.parse(c.end || c.start);
  return Number.isFinite(t) ? Math.max(0, (now - t) / 1000) : 0;
}

export type StripState = { mode: 'none' } | { mode: 'talking'; call: NowCall } | { mode: 'idle'; call: NowCall };

/** What the strip shows: the talker, else the last one heard, else nothing. */
export function stripState(n: NowCalling | null): StripState {
  if (n?.current) return { mode: 'talking', call: n.current };
  const last = n?.recent?.[0];
  return last ? { mode: 'idle', call: last } : { mode: 'none' };
}

/** "Last: N0CALL · AllStar · 5 min ago". */
export function idleLine(c: NowCall, now: number): string {
  return `Last: ${who(c)} · ${sourceShort(c)} · ${agoText(sinceSec(c, now))}`;
}

/** The spoken label of the strip button. */
export function ariaLabel(s: StripState, now: number): string {
  if (s.mode === 'talking') return `Now calling: ${mainLine(s.call)}, ${sourceLabel(s.call)}, ${mmss(durationSec(s.call, now) ?? 0)}. Open details.`;
  if (s.mode === 'idle') return `${idleLine(s.call, now)}. Open recent calls.`;
  return '';
}

/** One row of the details sheet. */
export interface SheetRow { label: string; value: string; cls?: string; note?: string }
