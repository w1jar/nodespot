// Pure helpers for the live input meter, the tuning wizard and the echo test report on the Audio tab.
import type { AutosetStatus, EchoReport, RxSummary } from './types';

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

/** The slider position for a chip control: its hardware step count (0 or absent: no steps, use percent). */
export const hasSteps = (steps: number | undefined): steps is number => typeof steps === 'number' && Number.isInteger(steps) && steps > 0;

/** A step's gain as text: "+12.0 dB", "-3.5 dB". */
export function stepDbText(db: number): string {
  return `${db > 0 ? '+' : ''}${db.toFixed(1)} dB`;
}

/** The slider label text for a hardware step: "+12.0 dB" when the dB is known, else "step 9 of 35". */
export function stepText(step: number, steps: number, db?: number | null): string {
  return typeof db === 'number' && Number.isFinite(db) ? stepDbText(db) : `step ${step} of ${steps}`;
}

/** The slider's step as the fraction the node accepts: exactly the step on the control's own grid. */
export function stepFraction(step: number, steps: number): number {
  if (!Number.isFinite(step) || !(steps > 0)) return 0;
  return Math.min(steps, Math.max(0, Math.round(step))) / steps;
}

/** The step a reported fraction (0..1) sits on. */
export function fractionStep(v: number, steps: number): number {
  return levelAvailable(v) ? Math.min(steps, Math.max(0, Math.round(v * steps))) : 0;
}

export type RxVerdictKind = 'none' | 'good' | 'quiet' | 'distort';
export interface RxVerdict { kind: RxVerdictKind; text: string }

/** The input is at or above this peak level: counts as distorting even without a counted clipped sample. */
export const CLIP_PEAK_DB = -1;
/** With the AGC off, speech under this level is too quiet. */
export const QUIET_SPEECH_DB = -35;

/**
 * The plain verdict on the last transmission: one sentence and what to do.
 * Distortion comes first (turning up would make it worse), then too quiet (the AGC ran out of gain, or it is off and the speech is low).
 */
export function rxVerdict(s: RxSummary | undefined, agcOn: boolean): RxVerdict {
  if (!s) return { kind: 'none', text: 'Key up and talk for a few seconds.' };
  if (s.clipped > 0 || s.peakDb >= CLIP_PEAK_DB) return { kind: 'distort', text: 'Distorting: move the slider left a little, then talk again.' };
  if ((agcOn && s.agcAtMax) || (!agcOn && s.speechDb < QUIET_SPEECH_DB)) return { kind: 'quiet', text: 'Too quiet: move the slider right a little, then talk again.' };
  return { kind: 'good', text: 'Sounds good. Nothing to change.' };
}

/** The signed dB text of the AGC's gain: "+6 dB". */
const gainText = (db: number): string => `${db > 0 ? '+' : ''}${db.toFixed(0)} dB`;

/** The numbers behind the verdict, as lines for the Details section. */
export function rxDetails(s: RxSummary, agcOn: boolean, agcMaxDb: number): string[] {
  const n = (db: number) => (Number.isFinite(db) && db > -90 ? `${Math.round(db)} dBFS` : '–');
  const lines = [`Length: ${durationText(s.durationMs)}`, `Loudest peak: ${n(s.peakDb)}`, `Speech level: ${n(s.speechDb)}`];
  if (agcOn) {
    lines.push(`AGC added: ${gainText(s.agcGainDb)}${s.agcAtMax ? ` (its +${Math.round(agcMaxDb)} dB limit)` : ''}`);
    lines.push(`Sent at: ${n(s.outDb)}`);
  }
  if (s.clipped > 0) lines.push(`Clipped samples: ${s.clipped}`);
  return lines;
}

/** The status line of the receive auto-set; '' when idle. */
export function autosetText(st: AutosetStatus | null): string {
  if (!st) return '';
  switch (st.state) {
    case 'waiting': return 'Key up and talk normally…';
    case 'measuring': return 'Listening… keep talking.';
    case 'failed': return st.error || 'Auto-set failed.';
    case 'done': {
      const r = st.result;
      if (!r) return 'Done.';
      return `Done: receive level set to ${stepText(r.step, r.steps, r.db)}`;
    }
    default: return '';
  }
}

/** True while an auto-set runs and can still be cancelled. */
export const autosetRunning = (st: AutosetStatus | null): boolean => st?.state === 'waiting' || st?.state === 'measuring';
