// Host lists: the master pickers of the DMR settings and the Host lists card. Pure functions, tested in hostlists.test.ts.
import type { HostItem, HostListsStatus } from './types';

/** The value of a master dropdown's "Other…" entry: the address is typed instead of picked. */
export const MASTER_OTHER = '__other__';

type Kind = 'bm' | 'dmr-other';

/** The port a master without one in its address uses: BrandMeister 54006 (the list's port is the Homebrew one and is not used), the others 62031. */
export const DEFAULT_PORT: Record<Kind, number> = { bm: 54006, 'dmr-other': 62031 };

/** A saved "host" or "host:port" split up; port 0 when there is none. */
export function splitAddress(address: string): { host: string; port: number } {
  const a = address.trim();
  const i = a.lastIndexOf(':');
  if (i > 0 && !a.slice(0, i).includes(':') && /^\d{1,5}$/.test(a.slice(i + 1))) return { host: a.slice(0, i), port: Number(a.slice(i + 1)) };
  return { host: a, port: 0 };
}

/** The dropdown choice for a saved address: '' when none is saved, the list entry (its value) the address names, else "Other…". */
export function masterChoice(kind: Kind, address: string, items: readonly HostItem[]): string {
  const a = address.trim();
  if (a === '') return '';
  const s = splitAddress(a);
  const port = s.port || DEFAULT_PORT[kind];
  // BrandMeister entries carry no usable port: only the engine's 54006 selects one.
  const hit = items.find((it) => (it.address || it.value).toLowerCase() === s.host.toLowerCase() && port === (kind === 'bm' ? DEFAULT_PORT.bm : it.port || DEFAULT_PORT[kind]));
  return hit ? hit.value : MASTER_OTHER;
}

/** What picking an entry writes into the address field: BrandMeister the host alone, the others host:port when the list gives a port. */
export function masterAddress(kind: Kind, it: HostItem): string {
  const host = it.address || it.value;
  return kind === 'dmr-other' && it.port > 0 ? `${host}:${it.port}` : host;
}

/** The field changes of picking an entry: the address, and the list's password when the form has a password field that is empty with none saved (never for BrandMeister). */
export function masterPick(kind: Kind, it: HostItem, draft: Record<string, string>, passwordSaved: boolean): Record<string, string> {
  const out: Record<string, string> = { address: masterAddress(kind, it) };
  if (kind === 'dmr-other' && it.password && 'password' in draft && (draft.password ?? '') === '' && !passwordSaved) out.password = it.password;
  return out;
}

/** The label of a list entry in the dropdown. */
export function masterLabel(kind: Kind, it: HostItem): string {
  if (kind === 'bm') return it.name;
  const host = masterAddress(kind, it);
  return it.name && it.name !== it.value ? `${it.name} (${host})` : host;
}

/** One entry of a master combobox: value is what a pick writes into the field (masterAddress), name the label, item the list entry. */
export interface MasterOption { value: string; name: string; detail?: string; item: HostItem }

/** The combobox entries of a master list. The second line names the host when the label does not already show it. */
export function masterOptions(kind: Kind, items: readonly HostItem[]): MasterOption[] {
  return items.map((it) => {
    const value = masterAddress(kind, it), name = masterLabel(kind, it);
    const detail = [name.includes(value) ? '' : value, it.detail ?? ''].filter(Boolean).join(' · ');
    return { value, name, detail: detail || undefined, item: it };
  });
}

/** The entries matching typed text: every word must appear in the name (which carries the country), the host or the detail. */
export function masterSearch(opts: readonly MasterOption[], text: string, limit = 50): MasterOption[] {
  const words = text.toLowerCase().split(/\s+/).filter(Boolean);
  return opts.filter((o) => {
    const hay = `${o.name} ${o.value} ${o.item.address} ${o.detail ?? ''}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  }).slice(0, limit);
}

/** The line under a master combobox: which list entry the saved address is, that it is used as typed, or nothing when empty. */
export function masterHint(kind: Kind, address: string, items: readonly HostItem[]): string {
  const c = masterChoice(kind, address, items);
  if (c === '') return '';
  if (c === MASTER_OTHER) return 'Not in the list: used as typed.';
  const it = items.find((x) => x.value === c);
  return it ? `From the list: ${masterLabel(kind, it)}` : '';
}

/** The note under a target search whose list is empty: the list has not been downloaded. */
export function emptyListNote(mode: string, title: string): string {
  const what = mode === 'p25' || mode === 'nxdn' ? 'talkgroup' : 'reflector';
  return `The ${title} ${what} list hasn't downloaded yet. See Settings → Station → Host lists.`;
}

/** The note under a master field whose list is empty. */
export function masterListNote(kind: Kind): string {
  return `The ${kind === 'bm' ? 'BrandMeister master' : 'master'} list hasn't downloaded yet, so type the address. See Settings → Station → Host lists.`;
}

export interface ListRow { id: string; label: string; count: number; updated: string; error: string }

const ROWS: [string, string][] = [['dmr', 'DMR masters'], ['ysf', 'YSF reflectors'], ['p25', 'P25 talkgroups'], ['nxdn', 'NXDN talkgroups'], ['m17', 'M17 reflectors']];

/** The rows of the Host lists card, AllStar nodes first. A list that was never fetched has no update time. */
export function listRows(st: HostListsStatus | null): ListRow[] {
  if (!st) return [];
  const rows: ListRow[] = [];
  if (st.nodelist) rows.push({ id: 'nodelist', label: 'ASL nodes', count: st.nodelist.count, updated: st.nodelist.updated ?? '', error: st.nodelist.error ?? '' });
  for (const [id, label] of ROWS) {
    const l = st.lists?.[id];
    rows.push({ id, label, count: l?.entries ?? 0, updated: l?.updated ?? '', error: l?.error ?? '' });
  }
  return rows;
}

/** An update time for people: the local date and time, or "never". */
export function whenText(iso: string): string {
  if (!iso) return 'never';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? 'never' : d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
}

/** Seconds until the next manual refresh is allowed (0: now). */
export function refreshWait(nextManual: string | undefined, now: number): number {
  if (!nextManual) return 0;
  const t = new Date(nextManual).getTime();
  return Number.isNaN(t) ? 0 : Math.max(0, Math.ceil((t - now) / 1000));
}
