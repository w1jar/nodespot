// The radio interface: presets, pin overrides and what the forms show. Pure functions, tested in radio.test.ts.
import type { RadioInfo, RadioInterface, RadioPreset, Station } from './types';

type RadioSettings = Station['radio'];

/** Carrier and tone sources the USB chip's input pins offer. */
export const COS_SOURCES: { value: string; label: string }[] = [
  { value: 'none', label: 'None (squelch is not read)' },
  { value: 'always', label: 'Always open' },
  { value: 'gpio1', label: 'GPIO 1 (volume up)' },
  { value: 'gpio2', label: 'GPIO 2 (volume down)' },
  { value: 'gpio3', label: 'GPIO 3 (mute playback)' },
  { value: 'gpio4', label: 'GPIO 4 (mute record)' },
];
export const CTCSS_SOURCES: { value: string; label: string }[] = [
  { value: 'none', label: 'None (no tone input)' },
  ...COS_SOURCES.filter((s) => s.value.startsWith('gpio')),
];
export const PTT_PINS = [1, 2, 3, 4];

/** The override fields of the station's radio settings, which an empty value leaves to the preset. */
const OVERRIDE_KEYS = ['pttGpio', 'pttActiveLow', 'cosSource', 'cosActiveLow', 'ctcssSource', 'ctcssActiveLow', 'sa818'] as const;

/** The preset a saved name stands for: "" is automatic, the SHARI when one was found, else none. */
export function presetName(name: string | undefined, shari: boolean): string {
  const n = (name ?? '').trim();
  if (n) return n;
  return shari ? 'shari' : 'none';
}

export function findPreset(presets: RadioPreset[], name: string): RadioPreset | undefined {
  return presets.find((p) => p.name === name);
}

/** The settings the radio section shows: the preset's with the saved overrides on top. Undefined while the presets are unknown. */
export function resolveInterface(r: RadioSettings, presets: RadioPreset[], shari: boolean): RadioInterface | undefined {
  const p = findPreset(presets, presetName(r.preset, shari));
  if (!p) return undefined;
  const d = p.defaults;
  if (!d.audio) return { ...d };
  return {
    ...d,
    pttPin: r.pttGpio || d.pttPin,
    pttActiveLow: r.pttActiveLow ?? d.pttActiveLow,
    cos: r.cosSource || d.cos,
    cosActiveLow: r.cosActiveLow ?? d.cosActiveLow,
    ctcss: r.ctcssSource || d.ctcss,
    ctcssActiveLow: r.ctcssActiveLow ?? d.ctcssActiveLow,
    sa818: r.sa818 ?? d.sa818,
  };
}

/** True when the node programs an SA818 itself, so the frequency, tone and squelch belong on this page. */
export const hasSA818 = (i: RadioInterface | undefined): boolean => !!i && i.audio && i.sa818;

/** Picks a preset and drops the overrides, so the preset's own settings apply. */
export function pickPreset(r: RadioSettings, name: string): void {
  r.preset = name;
  for (const k of OVERRIDE_KEYS) delete r[k];
}

/** The radio settings to save: overrides equal to the preset's are dropped, and none of them are kept for the none preset. */
export function radioBody(r: RadioSettings, presets: RadioPreset[], shari: boolean): RadioSettings {
  const out: RadioSettings = { ...r };
  const p = findPreset(presets, presetName(r.preset, shari));
  const keep = !!p?.defaults.audio;
  const d = p?.defaults;
  if (!keep || !d) { for (const k of OVERRIDE_KEYS) delete out[k]; return out; }
  const same: Record<(typeof OVERRIDE_KEYS)[number], unknown> = {
    pttGpio: d.pttPin, pttActiveLow: d.pttActiveLow, cosSource: d.cos, cosActiveLow: d.cosActiveLow,
    ctcssSource: d.ctcss, ctcssActiveLow: d.ctcssActiveLow, sa818: d.sa818,
  };
  for (const k of OVERRIDE_KEYS) {
    if (out[k] === undefined || out[k] === null || (out[k] as unknown) === '' || out[k] === same[k]) delete out[k];
  }
  return out;
}

/** An error text when the pins collide, or ''. The node checks the same rules. */
export function pinError(i: RadioInterface | undefined): string {
  if (!i || !i.audio) return '';
  const pin = (s: string) => (/^gpio[1-4]$/.test(s) ? +s.slice(4) : 0);
  const c = pin(i.cos), t = pin(i.ctcss);
  if (c && c === i.pttPin) return 'The PTT pin and the squelch input cannot be the same pin';
  if (t && t === i.pttPin) return 'The PTT pin and the tone input cannot be the same pin';
  if (c && c === t) return 'The squelch and tone inputs cannot be the same pin';
  return '';
}

/** One line about the detected interface, e.g. "CM108B (0d8c:0012)". Empty when none was found. */
export function detectedLabel(i: { chip?: string; usbId?: string } | undefined): string {
  if (!i?.chip && !i?.usbId) return '';
  return [i.chip, i.usbId ? `(${i.usbId})` : ''].filter(Boolean).join(' ');
}

/** True when a SHARI is among what was found (decides what automatic means). */
export const isShari = (info: Pick<RadioInfo, 'shari'> | null | undefined): boolean => !!info?.shari;
