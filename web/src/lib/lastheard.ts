// The rows of the Last heard table (LastHeardTable.svelte), shared by the Dashboard and the public page.
// A call of any mode ends up in the same shape: callsign, mode, target, when, and the lookup links the callsign opens.
import { modeTitle } from './modes.ts';
import { placeText, qrzUrl, radioidUrl } from './nowcalling.ts';
import type { Heard, NowCall, PubSnapshot } from './types';

export interface HeardItem {
  key: string;
  /** What the callsign cell shows ("N0CALL", a DMR ID, "Echo test", "Private call"). */
  call: string;
  /** "AllStar", "BrandMeister", "YSF", ... */
  mode: string;
  /** "TG 91 · TS2", "YSF-TEST" (the Mode column says YSF), "Node 2001"; '' when not known. */
  target: string;
  /** When it was heard (ms); 0 when unknown. */
  at: number;
  /** The caller's name and place for the muted second line under the callsign ("Pat Example · Springfield, XX"); '' when not known. */
  person: string;
  /** Tooltip for the row. */
  title?: string;
  /** Lookup links; '' when there is nothing to look up (a pseudo-callsign, no DMR ID). */
  qrz: string;
  radioid: string;
}

/** The second line under a callsign: the name and the place, whichever are known. */
export const personLine = (name: string | undefined, place: string | undefined): string => [name, place].map((x) => (x ?? '').trim()).filter(Boolean).join(' · ');

/** A callsign worth a lookup: letters and a digit, so "Echo test", a bare DMR ID and "Private call" are not. */
export function lookupCall(call: string | undefined): string {
  const c = (call ?? '').trim();
  return /[A-Za-z]/.test(c) && /\d/.test(c) ? qrzUrl(c) : '';
}

const parseAt = (s: string | undefined): number => { const t = s ? Date.parse(s.replace(' ', 'T')) : NaN; return Number.isFinite(t) ? t : 0; };

/** The "target" cell: a talkgroup reads "TG 91", an AllStar node "Node 2001", others as they are. */
export function targetCell(mode: string, target: string | undefined): string {
  const t = (target ?? '').trim();
  if (!t) return '';
  if (mode === 'bm' || mode === 'dmr' || mode === 'tgif') return `TG ${t}`;
  if (mode === 'allstar' && /^\d+$/.test(t)) return `Node ${t}`;
  return t;
}

/** GET /api/heard rows (every mode) as table rows. */
export function heardItems(heard: Heard[] | undefined): HeardItem[] {
  return (heard ?? []).map((h, i) => {
    const call = (h.callsign ?? '').trim();
    return {
      key: `h:${i}:${h.start}`,
      call: call || (h.id ? `ID ${h.id}` : 'Unknown'),
      mode: modeTitle(h.mode),
      target: targetCell(h.mode, h.target),
      at: parseAt(h.end || h.start),
      person: personLine(h.name, h.place),
      title: h.id ? `ID ${h.id}` : undefined,
      qrz: lookupCall(call),
      radioid: h.id ? radioidUrl({ kind: h.mode, dmrId: String(h.id) }) : '',
    };
  });
}

/** Now Calling's recent list (of every mode) as table rows. */
export function callItems(recent: NowCall[] | undefined): HeardItem[] {
  return (recent ?? []).map((c) => {
    const call = (c.call ?? '').trim();
    return {
      key: `c:${c.id}`,
      call: call || (c.dmrId ? `ID ${c.dmrId}` : c.node ? `Node ${c.node}` : 'Unknown'),
      mode: modeTitle(c.kind),
      target: targetCell(c.kind, c.target),
      at: parseAt(c.end || c.start),
      person: c.kind === 'allstar' ? (c.desc ?? '').trim() : personLine(c.first || c.name, placeText(c)),
      title: (c.targetName ?? '').trim() || undefined,
      qrz: lookupCall(call),
      radioid: radioidUrl(c),
    };
  });
}

/** The one Last heard list: the node's heard log and the strip's recent calls, newest first, without the same call twice. */
export function mergeHeard(recent: NowCall[] | undefined, heard: Heard[] | undefined, limit = 12): HeardItem[] {
  const all = [...heardItems(heard), ...callItems(recent)].sort((a, b) => b.at - a.at);
  const seen = new Set<string>(), out: HeardItem[] = [];
  for (const r of all) {
    const k = `${r.call}|${r.mode}|${Math.round(r.at / 5000)}`;
    if (seen.has(k)) continue;
    seen.add(k); out.push(r);
  }
  return out.slice(0, limit);
}

/** The public page's Last heard rows, newest first. Only what the public API sends: a callsign opens QRZ and nothing opens radioid.net. */
export function pubHeardItems(snap: Pick<PubSnapshot, 'heard' | 'showHeard'> | null | undefined, limit = 12): HeardItem[] {
  if (!snap?.showHeard) return [];
  return (snap.heard ?? []).map((c, i) => ({
    key: `p:${i}:${c.at}`, call: c.callsign || `${modeTitle(c.mode)} caller`, mode: modeTitle(c.mode), target: targetCell(c.mode, c.target),
    at: c.at * 1000, person: personLine(c.name, c.location), qrz: lookupCall(c.callsign), radioid: '',
  })).sort((a, b) => b.at - a.at).slice(0, limit);
}
