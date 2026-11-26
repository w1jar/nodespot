
export function fmtDuration(total: number): string {
  total = Math.max(0, Math.floor(total));
  const h = Math.floor(total / 3600), m = Math.floor((total % 3600) / 60), s = total % 60;
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(h)}:${p(m)}:${p(s)}`;
}
export function fmtUptime(total: number): string {
  total = Math.max(0, Math.floor(total));
  const d = Math.floor(total / 86400), h = Math.floor((total % 86400) / 3600), m = Math.floor((total % 3600) / 60);
  return d > 0 ? `${d}d ${h}h` : h > 0 ? `${h}h ${m}m` : `${m}m`;
}
export function fmtAgo(sec: number): string {
  sec = Math.max(0, Math.floor(sec));
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return `${Math.floor(sec / 86400)}d ago`;
}
/** "KeyupsSinceSystemInitialization" -> "Keyups Since System Initialization". */
export function humanizeKey(k: string): string {
  const words = k.replace(/([a-z0-9])([A-Z])/g, '$1 $2').replace(/([A-Z]+)([A-Z][a-z])/g, '$1 $2').split(' ');
  const acr: Record<string, string> = { Dtmf: 'DTMF', Tx: 'TX', Rx: 'RX', Iaxrpt: 'IAXRPT' };
  return words.map((w) => acr[w] ?? w).join(' ');
}
export const cToF = (c: number) => c * 9 / 5 + 32;

export interface NoteInline { text: string; bold: boolean }
export type NoteBlock = { kind: 'p'; lines: NoteInline[][] } | { kind: 'ul'; items: NoteInline[][] };

function inline(text: string): NoteInline[] {
  const out: NoteInline[] = [];
  text.split(/\*\*(.+?)\*\*/g).forEach((part, i) => { if (part) out.push({ text: part, bold: i % 2 === 1 }); });
  return out;
}

/** Markdown-lite for release notes: paragraphs, "- " lists, **bold**. Returns data, never HTML. */
export function parseNotes(src: string): NoteBlock[] {
  const blocks: NoteBlock[] = [];
  for (const raw of src.replace(/\r/g, '').split('\n')) {
    const line = raw.trim();
    if (!line) { blocks.push({ kind: 'p', lines: [] }); continue; }
    const m = /^[-*]\s+(.*)$/.exec(line);
    const last = blocks[blocks.length - 1];
    if (m) {
      if (last?.kind === 'ul') last.items.push(inline(m[1])); else blocks.push({ kind: 'ul', items: [inline(m[1])] });
    } else {
      const t = line.replace(/^#{1,6}\s+/, '');
      if (last?.kind === 'p') last.lines.push(inline(t)); else blocks.push({ kind: 'p', lines: [inline(t)] });
    }
  }
  return blocks.filter((b) => (b.kind === 'ul' ? b.items.length : b.lines.length));
}

export const REPO_URL = 'https://github.com/w1jar/nodespot';
/** Issue form with fields prefilled; the keys are the field ids of .github/ISSUE_TEMPLATE/bug_report.yml. */
export function bugReportUrl(f: { version?: string; asl?: string; hardware?: string; os?: string } = {}): string {
  const p = new URLSearchParams({ template: 'bug_report.yml' });
  for (const [k, v] of Object.entries(f)) if (v) p.set(k, v);
  return `${REPO_URL}/issues/new?${p}`;
}
export const featureRequestUrl = `${REPO_URL}/issues/new?template=feature_request.yml`;
export const releasesUrl = `${REPO_URL}/releases`;

/** "just now", "42s ago", "3 min ago", "2 h ago", "4 d ago". */
export function fmtAgoLong(sec: number): string {
  sec = Math.max(0, Math.floor(sec));
  if (sec < 10) return 'just now';
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)} min ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} h ago`;
  return `${Math.floor(sec / 86400)} d ago`;
}
