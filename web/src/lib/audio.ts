// The "Audio and timers" card: limits, defaults and checks for station.advanced and station.id. Pure functions, tested in
// audio.test.ts. The limits match Validate in internal/native/node/config.go; 0 or unset means the node's default.
import type { Station, StationAdvanced, StationId } from './types';

export interface NumField { key: string; label: string; unit: string; min: number; max: number; step: number; def: number; help: string }

/** Defaults of the node (config.go and audio/agc.go). */
export const AGC_DEFAULTS = { modeAgc: true, radioAgc: false, courtesyTone: true } as const;

export const AGC_FIELDS: NumField[] = [
  { key: 'agcTargetDb', label: 'Target level', unit: 'dBFS', min: -40, max: -6, step: 1, def: -22, help: 'Speech level the AGC aims for, -40 to -6.' },
  { key: 'agcMaxDb', label: 'Maximum gain', unit: 'dB', min: 1, max: 30, step: 1, def: 18, help: 'The most the AGC may boost quiet audio, 1 to 30.' },
];
export const TIMER_FIELDS: NumField[] = [
  { key: 'hangMs', label: 'Hang time', unit: 'ms', min: 1, max: 10000, step: 10, def: 500, help: 'How long the transmitter stays keyed after the audio stops, 1 to 10000.' },
  { key: 'txTimeoutS', label: 'Transmit timeout', unit: 's', min: 1, max: 1800, step: 1, def: 180, help: 'Longest single transmission, from the radio or from the network, 1 to 1800.' },
];
export const ID_FIELDS: NumField[] = [
  { key: 'intervalS', label: 'ID interval', unit: 's', min: 1, max: 600, step: 1, def: 600, help: 'Longest time from the first transmission to the ID, at most 600 (10 minutes).' },
  { key: 'tailWindowS', label: 'Tail window', unit: 's', min: 1, max: 600, step: 1, def: 60, help: 'The ID may ride the tail of a transmission this long before the deadline.' },
  { key: 'endQuietS', label: 'Final ID after quiet', unit: 's', min: 1, max: 600, step: 1, def: 60, help: 'The last ID plays after this long without transmitting.' },
  { key: 'wpm', label: 'CW speed', unit: 'WPM', min: 5, max: 60, step: 1, def: 25, help: '5 to 60.' },
  { key: 'pitchHz', label: 'CW pitch', unit: 'Hz', min: 200, max: 2000, step: 10, def: 800, help: '200 to 2000.' },
  { key: 'levelDb', label: 'CW level', unit: 'dBFS', min: -60, max: -1, step: 1, def: -24, help: '-60 to -1.' },
];

/** Whether a field value is set: not empty. */
function isSet(v: unknown): boolean { return v != null && !(typeof v === 'string' && v.trim() === ''); }

/** One number against its field: '' when empty (default) or valid, else an error text. */
export function fieldError(f: NumField, v: unknown): string {
  if (!isSet(v)) return '';
  const n = typeof v === 'number' ? v : Number(String(v).trim());
  if (!Number.isFinite(n) || n < f.min || n > f.max) return `${f.label} must be ${f.min} to ${f.max} ${f.unit}, or empty for ${f.def}`;
  if (f.step >= 1 && !Number.isInteger(n)) return `${f.label} must be a whole number`;
  return '';
}

/** The whole card: an error text or ''. */
export function audioError(adv: StationAdvanced, id: StationId): string {
  for (const f of [...AGC_FIELDS, ...TIMER_FIELDS]) { const e = fieldError(f, adv[f.key]); if (e) return e; }
  for (const f of ID_FIELDS) { const e = fieldError(f, id[f.key]); if (e) return e; }
  const v = (id.voiceFile ?? '').trim();
  if (v !== '' && !v.startsWith('/')) return 'Voice file must be an absolute path, such as /var/lib/nodespot/id.wav';
  return '';
}

/** Copy of o with the number keys cleaned: empty and 0 are left out (the node's default), the rest are numbers. */
function cleanNums<T extends Record<string, unknown>>(o: T, fields: NumField[]): T {
  const out: Record<string, unknown> = { ...o };
  for (const f of fields) {
    const v = out[f.key];
    if (!isSet(v)) { delete out[f.key]; continue; }
    const n = Number(typeof v === 'string' ? v.trim() : v);
    if (n === 0 || !Number.isFinite(n)) delete out[f.key]; else out[f.key] = n;
  }
  return out as T;
}

/** A switch to save: left out when it is not a boolean or equals the default (the same thing to the node). */
function cleanBool(o: Record<string, unknown>, key: keyof typeof AGC_DEFAULTS): void {
  if (typeof o[key] !== 'boolean' || o[key] === AGC_DEFAULTS[key]) delete o[key];
}

/** The station to save: `base` (the latest saved station) with advanced and id replaced by the edited ones; every other key,
 * known or not, is carried over. */
export function audioBody(base: Station, adv: StationAdvanced, id: StationId): Station {
  const a = cleanNums({ ...adv }, [...AGC_FIELDS, ...TIMER_FIELDS]) as Record<string, unknown>;
  for (const k of Object.keys(AGC_DEFAULTS) as (keyof typeof AGC_DEFAULTS)[]) cleanBool(a, k);
  const i = cleanNums({ ...id }, ID_FIELDS) as Record<string, unknown>;
  if (typeof i.voiceFile === 'string') { const v = i.voiceFile.trim(); if (v === '') delete i.voiceFile; else i.voiceFile = v; }
  if (i.enabled === true) delete i.enabled; // on is the default
  return { ...base, advanced: a as StationAdvanced, id: i as StationId };
}

/** Whether the edited card differs from the saved one (compared as they would be saved). */
export function audioDirty(base: Station, adv: StationAdvanced, id: StationId): boolean {
  const a = audioBody(base, base.advanced, base.id), b = audioBody(base, adv, id);
  return JSON.stringify([sortKeys(a.advanced), sortKeys(a.id)]) !== JSON.stringify([sortKeys(b.advanced), sortKeys(b.id)]);
}
function sortKeys(o: Record<string, unknown>): Record<string, unknown> { return Object.fromEntries(Object.keys(o).sort().map((k) => [k, o[k]])); }
