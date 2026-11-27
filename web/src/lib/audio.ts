// The Radio and Audio tabs of Settings, Station: limits, defaults and checks for station.advanced and station.id. Pure functions, tested in
// audio.test.ts. The limits match Validate in internal/native/node/config.go; 0 or unset means the node's default.
import type { Sa818Filter, Station, StationAdvanced, StationId } from './types';

/** keepZero: 0 is a value of its own (off), saved as such; only empty is the node's default. */
export interface NumField { key: string; label: string; unit: string; min: number; max: number; step: number; def: number; help: string; keepZero?: boolean }

/** Defaults of the node (config.go and audio/agc.go). */
export const AGC_DEFAULTS = { modeAgc: true, radioAgc: true, flatAudio: false } as const;

/** The SA818 filter flags and the node's defaults when none is stored: emphasis off, high-pass and low-pass on. */
export const SA818_FILTER_KEYS = ['emphasis', 'highPass', 'lowPass'] as const;
export const SA818_FILTER_DEFAULTS = { emphasis: false, highPass: true, lowPass: true } as const;

/** A filter flag as the node will program it: the stored one, else the default. */
export const filterOn = (f: Sa818Filter | undefined, k: (typeof SA818_FILTER_KEYS)[number]): boolean => f?.[k] ?? SA818_FILTER_DEFAULTS[k];

/** Static gain, radio to network (rx) and network to radio (tx), applied before the AGC. */
export const GAIN_FIELDS: NumField[] = [
  { key: 'rxGainDb', label: 'Receive gain', unit: 'dB', min: -20, max: 20, step: 0.5, def: 0, help: 'Radio to network, -20 to 20. Applied before the AGC; peaks are soft-limited, never wrapped.' },
  { key: 'txGainDb', label: 'Transmit gain', unit: 'dB', min: -20, max: 20, step: 0.5, def: 0, help: 'Network to radio, -20 to 20. Applied before the AGC.' },
];
/** Delays: how long a carrier must last before it counts as a receive, how long the transmitter is keyed before network
 * audio plays, and extra time the transmitter stays keyed. */
export const DELAY_FIELDS: NumField[] = [
  { key: 'rxOnDelayMs', label: 'Receive-on delay', unit: 'ms', min: 0, max: 1000, step: 10, def: 0, help: 'A carrier must last this long before the node treats it as a receive. The first part of the audio is dropped. 0 to 1000.' },
  { key: 'txOnDelayMs', label: 'Key-up delay', unit: 'ms', min: 0, max: 1000, step: 10, def: 500, keepZero: true, help: 'Network audio waits this long after the transmitter keys, so the radio is up before the first word. Empty is 500; 0 turns it off. 0 to 1000.' },
  { key: 'txOffDelayMs', label: 'Transmit-off delay', unit: 'ms', min: 0, max: 2000, step: 10, def: 0, help: 'Extra time the transmitter stays keyed after the hang time, 0 to 2000.' },
];

/** The AGC targets, one per direction. */
export const AGC_TARGET_FIELDS: NumField[] = [
  { key: 'rxAgcTargetDb', label: 'Radio → network target', unit: 'dBFS', min: -40, max: -6, step: 1, def: -22, help: 'Speech level sent to the network, -40 to -6.' },
  { key: 'txAgcTargetDb', label: 'Network → radio target', unit: 'dBFS', min: -40, max: -6, step: 1, def: -22, help: 'Speech level sent to the transmitter, -40 to -6.' },
];
export const AGC_FIELDS: NumField[] = [
  ...AGC_TARGET_FIELDS,
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
  for (const f of [...GAIN_FIELDS, ...AGC_FIELDS, ...TIMER_FIELDS, ...DELAY_FIELDS]) { const e = fieldError(f, adv[f.key]); if (e) return e; }
  for (const f of ID_FIELDS) { const e = fieldError(f, id[f.key]); if (e) return e; }
  const v = (id.voiceFile ?? '').trim();
  if (v !== '' && !v.startsWith('/')) return 'Voice file must be an absolute path, such as /var/lib/nodespot/id.wav';
  return '';
}

/** Copy of o with the number keys cleaned: empty and 0 are left out (the node's default), the rest are numbers. A keepZero
 * field keeps 0. */
function cleanNums<T extends Record<string, unknown>>(o: T, fields: NumField[]): T {
  const out: Record<string, unknown> = { ...o };
  for (const f of fields) {
    const v = out[f.key];
    if (!isSet(v)) { delete out[f.key]; continue; }
    const n = Number(typeof v === 'string' ? v.trim() : v);
    if ((n === 0 && !f.keepZero) || !Number.isFinite(n)) delete out[f.key]; else out[f.key] = n;
  }
  return out as T;
}

/** A switch to save: left out when it is not a boolean or equals the default (the same thing to the node). */
function cleanBool(o: Record<string, unknown>, key: keyof typeof AGC_DEFAULTS): void {
  if (typeof o[key] !== 'boolean' || o[key] === AGC_DEFAULTS[key]) delete o[key];
}

/** The station to save: `base` (the latest saved station) with advanced and id replaced by the edited ones; every other key,
 * known or not, is carried over. */
export function audioBody(base: Station, adv: StationAdvanced, id: StationId, filt?: Sa818Filter): Station {
  const a = cleanNums({ ...adv }, [...GAIN_FIELDS, ...AGC_FIELDS, ...TIMER_FIELDS, ...DELAY_FIELDS]) as Record<string, unknown>;
  if (typeof a.plFilter !== 'boolean') delete a.plFilter; // unset is automatic
  for (const k of Object.keys(AGC_DEFAULTS) as (keyof typeof AGC_DEFAULTS)[]) cleanBool(a, k);
  const i = cleanNums({ ...id }, ID_FIELDS) as Record<string, unknown>;
  if (typeof i.voiceFile === 'string') { const v = i.voiceFile.trim(); if (v === '') delete i.voiceFile; else i.voiceFile = v; }
  if (i.enabled === true) delete i.enabled; // on is the default
  const out: Station = { ...base, advanced: a as StationAdvanced, id: i as StationId };
  if (filt !== undefined) {
    // Only flags that differ from the defaults are stored; the defaults are left out.
    const f: Sa818Filter = {};
    for (const k of SA818_FILTER_KEYS) if (typeof filt[k] === 'boolean' && filt[k] !== SA818_FILTER_DEFAULTS[k]) f[k] = filt[k];
    const radio = { ...base.radio } as Record<string, unknown>;
    if (Object.keys(f).length) radio.sa818Filter = f; else delete radio.sa818Filter;
    out.radio = radio as Station['radio'];
  }
  return out;
}

/** Whether the edited card differs from the saved one (compared as they would be saved). */
export function audioDirty(base: Station, adv: StationAdvanced, id: StationId, filt?: Sa818Filter): boolean {
  const a = audioBody(base, base.advanced, base.id, base.radio?.sa818Filter ?? {}), b = audioBody(base, adv, id, filt ?? base.radio?.sa818Filter ?? {});
  return JSON.stringify([sortKeys(a.advanced), sortKeys(a.id), sortKeys((a.radio?.sa818Filter ?? {}) as Record<string, unknown>)])
    !== JSON.stringify([sortKeys(b.advanced), sortKeys(b.id), sortKeys((b.radio?.sa818Filter ?? {}) as Record<string, unknown>)]);
}

/** The PL filter as the node will run it: the saved choice, else automatic (on unless the radio is an SA818). */
export function effectivePL(adv: StationAdvanced, sa818: boolean): boolean { return adv.plFilter ?? !sa818; }

/** What to show for the SA818's filters as last programmed: a text per flag, or 'unknown until programmed'. */
export function programmedText(p?: { emphasis: boolean; highPass: boolean; lowPass: boolean }): string {
  if (!p) return 'Unknown until programmed';
  return `Programmed: emphasis ${p.emphasis ? 'on' : 'off'}, high-pass ${p.highPass ? 'on' : 'off'}, low-pass ${p.lowPass ? 'on' : 'off'}`;
}
function sortKeys(o: Record<string, unknown>): Record<string, unknown> { return Object.fromEntries(Object.keys(o).sort().map((k) => [k, o[k]])); }

/** The station.advanced keys each Settings, Station tab edits. A tab saves only its own keys over the station as saved now,
 * so a change made on the other tab (or a tuning slider) is kept. */
export const RADIO_ADV_KEYS: readonly string[] = [...TIMER_FIELDS, ...DELAY_FIELDS].map((f) => f.key).concat(['flatAudio', 'plFilter']);
export const AUDIO_ADV_KEYS: readonly string[] = [...GAIN_FIELDS, ...AGC_FIELDS].map((f) => f.key).concat(['modeAgc', 'radioAgc']);

/** A copy of base where each of keys takes the edited value (left out when edited has none). */
export function withKeys(base: StationAdvanced, edited: StationAdvanced, keys: readonly string[]): StationAdvanced {
  const out: StationAdvanced = { ...base };
  for (const k of keys) { if (edited[k] === undefined) delete out[k]; else out[k] = edited[k]; }
  return out;
}
