// Pure helpers for the live input meter, the tuning wizard and the echo test report on the Audio tab.
import type { EchoReport } from './types';

/** The bottom of the meter, in dBFS. Anything quieter shows as an empty bar. */
export const METER_FLOOR_DB = -60;
/** The target band for speech peaks, in dBFS. */
export const TARGET_LOW_DB = -20;
export const TARGET_HIGH_DB = -6;
/** At or above this level the bar turns red. */
export const HOT_DB = -3;

/** Maps a dBFS value to 0..100 along the meter (METER_FLOOR_DB to 0). Not finite values count as the floor. */
export function barPercent(db: number): number {
  if (!Number.isFinite(db)) return 0;
  const p = ((db - METER_FLOOR_DB) / -METER_FLOOR_DB) * 100;
  return Math.min(100, Math.max(0, p));
}

/** The shaded target band as left and width percentages of the meter. */
export function targetBand(): { left: number; width: number } {
  const left = barPercent(TARGET_LOW_DB);
  return { left, width: barPercent(TARGET_HIGH_DB) - left };
}

/** True when the level is near full scale, where the bar is drawn red. */
export const isHot = (db: number): boolean => Number.isFinite(db) && db >= HOT_DB;

/** A dBFS value as text: "-12.3 dBFS", or a dash when there is no signal. */
export function dbText(db: number | undefined): string {
  if (db === undefined || !Number.isFinite(db) || db <= -90) return '–';
  return `${db.toFixed(1)} dBFS`;
}

/** A warning of the echo test report as a sentence the operator can act on. */
export function warningText(w: string): string {
  switch (w) {
    case 'clipping': return 'The audio clipped. Lower the receive volume.';
    case 'too quiet': return 'The audio is too quiet. Raise the receive volume.';
    default: return w;
  }
}

/** A duration in milliseconds as seconds with one decimal: "3.4 s". */
export const durationText = (ms: number): string => `${(Math.max(0, ms) / 1000).toFixed(1)} s`;

/** One line for the echo test report, or "" when none was recorded. */
export function reportSummary(r: EchoReport | undefined): string {
  if (!r) return '';
  const w = r.warnings?.length ? r.warnings.map(warningText).join(' ') : 'Levels look good.';
  return `Peak ${dbText(r.peakDb)}, RMS ${dbText(r.rmsDb)}, ${durationText(r.durationMs)}. ${w}`;
}

/** The node reports and accepts mixer levels as fractions 0..1 (-1 means the sound chip has no such control). The page shows percent. */
export const levelAvailable = (v: number): boolean => Number.isFinite(v) && v >= 0;

/** A node level (0..1) as a whole percent for the slider and label; unavailable or not finite counts as 0. */
export function levelPercent(v: number): number {
  return levelAvailable(v) ? Math.min(100, Math.round(v * 100)) : 0;
}

/** The slider's percent (0..100) as the fraction the node accepts; out of range values are clamped. */
export function levelFraction(pct: number): number {
  if (!Number.isFinite(pct)) return 0;
  return Math.min(100, Math.max(0, Math.round(pct))) / 100;
}

/** The label text for a node level: "60%", or "n/a" when the chip has no such control. */
export const levelText = (v: number): string => (levelAvailable(v) ? `${levelPercent(v)}%` : 'n/a');

/** The tuning verdict for a speech peak: Good inside the target band, else Too low or Too high; '' with no signal. */
export function levelVerdict(peakDb: number | undefined): '' | 'Good' | 'Too low' | 'Too high' {
  if (peakDb === undefined || !Number.isFinite(peakDb) || peakDb <= -90) return '';
  if (peakDb < TARGET_LOW_DB) return 'Too low';
  if (peakDb > TARGET_HIGH_DB) return 'Too high';
  return 'Good';
}

/** The highest peak of the recent readings (the meter's peak hold), or undefined when there are none. */
export function heldPeak(peaks: number[]): number | undefined {
  const f = peaks.filter((p) => Number.isFinite(p));
  return f.length ? Math.max(...f) : undefined;
}
