import test from 'node:test';
import assert from 'node:assert/strict';
import { barPercent, targetBand, isHot, dbText, warningText, durationText, reportSummary, levelPercent, levelFraction, levelText, levelAvailable, hasSteps, stepText, stepFraction, fractionStep, rxVerdict, rxDetails, autosetText, autosetRunning } from './levels.ts';

test('barPercent maps -60..0 dBFS to 0..100', () => {
  assert.equal(barPercent(0), 100);
  assert.equal(barPercent(-30), 50);
  assert.equal(barPercent(-60), 0);
  assert.equal(barPercent(-90), 0);
  assert.equal(barPercent(6), 100);
  assert.equal(barPercent(NaN), 0);
  assert.equal(barPercent(-Infinity), 0);
});

test('targetBand covers -20 to -6', () => {
  const b = targetBand();
  assert.ok(Math.abs(b.left - (40 / 60) * 100) < 1e-9);
  assert.ok(Math.abs(b.width - (14 / 60) * 100) < 1e-9);
});

test('isHot is near full scale only', () => {
  assert.equal(isHot(-1), true);
  assert.equal(isHot(-3), true);
  assert.equal(isHot(-10), false);
  assert.equal(isHot(NaN), false);
});

test('dbText and durationText', () => {
  assert.equal(dbText(-12.34), '-12.3 dBFS');
  assert.equal(dbText(-90), '–');
  assert.equal(dbText(undefined), '–');
  assert.equal(durationText(3400), '3.4 s');
  assert.equal(durationText(-5), '0.0 s');
});

test('warningText explains each warning', () => {
  assert.match(warningText('clipping'), /Lower the receive volume/);
  assert.match(warningText('too quiet'), /Raise the receive volume/);
  assert.equal(warningText('other'), 'other');
});

test('reportSummary', () => {
  assert.equal(reportSummary(undefined), '');
  const ok = { at: '2026-01-01T00:00:00Z', frames: 100, durationMs: 2000, peakDb: -10, rmsDb: -20, voicedFrames: 90, clipped: 0 };
  assert.equal(reportSummary(ok), 'Peak -10.0 dBFS, RMS -20.0 dBFS, 2.0 s. Levels look good.');
  assert.match(reportSummary({ ...ok, warnings: ['clipping'] }), /clipped/);
});

test('levels: the node speaks 0..1, the page percent', () => {
  assert.equal(levelPercent(0.6), 60);
  assert.equal(levelPercent(0), 0);
  assert.equal(levelPercent(1), 100);
  assert.equal(levelPercent(0.555), 56);
  assert.equal(levelPercent(-1), 0);
  assert.equal(levelPercent(NaN), 0);
  assert.equal(levelText(0.6), '60%');
  assert.equal(levelText(0), '0%');
  assert.equal(levelText(-1), 'n/a');
  assert.equal(levelAvailable(-1), false);
  assert.equal(levelAvailable(0), true);
  assert.equal(levelFraction(60), 0.6);
  assert.equal(levelFraction(100), 1);
  assert.equal(levelFraction(0), 0);
  assert.equal(levelFraction(150), 1);
  assert.equal(levelFraction(-5), 0);
  assert.equal(levelFraction(33.4), 0.33);
  // A round trip never drifts.
  for (let p = 0; p <= 100; p++) assert.equal(levelPercent(levelFraction(p)), p);
});

const rx = { at: '2026-01-01T00:00:00Z', durationMs: 4200, peakDb: -7, speechDb: -31, agcGainDb: 6, outDb: -25, agcAtMax: false, clipped: 0 };

test('rxVerdict: one plain sentence', () => {
  assert.deepEqual(rxVerdict(undefined, true), { kind: 'none', text: 'Key up and talk for a few seconds.' });
  assert.deepEqual(rxVerdict(rx, true), { kind: 'good', text: 'Sounds good. Nothing to change.' });
  assert.equal(rxVerdict({ ...rx, agcAtMax: true }, true).kind, 'quiet');
  assert.match(rxVerdict({ ...rx, agcAtMax: true }, true).text, /^Too quiet: move the slider right a little, then talk again\.$/);
  assert.match(rxVerdict({ ...rx, clipped: 3 }, true).text, /^Distorting: move the slider left a little, then talk again\.$/);
  // Peaks at full scale count even with no counted sample.
  assert.equal(rxVerdict({ ...rx, peakDb: -0.5 }, true).kind, 'distort');
  // Clipping wins over the AGC at its limit.
  assert.equal(rxVerdict({ ...rx, clipped: 1, agcAtMax: true }, true).kind, 'distort');
  // The AGC off: low speech is too quiet, and agcAtMax is ignored.
  assert.equal(rxVerdict({ ...rx, speechDb: -40, agcGainDb: 0 }, false).kind, 'quiet');
  assert.equal(rxVerdict({ ...rx, speechDb: -30, agcAtMax: true }, false).kind, 'good');
  assert.equal(rxVerdict({ ...rx, speechDb: -40 }, true).kind, 'good');
});

test('rxDetails lists the numbers', () => {
  assert.deepEqual(rxDetails(rx, true, 18), ['Length: 4.2 s', 'Loudest peak: -7 dBFS', 'Speech level: -31 dBFS', 'AGC added: +6 dB', 'Sent at: -25 dBFS']);
  const hot = rxDetails({ ...rx, agcGainDb: 18, agcAtMax: true, clipped: 12 }, true, 18);
  assert.ok(hot.includes('AGC added: +18 dB (its +18 dB limit)'));
  assert.ok(hot.includes('Clipped samples: 12'));
  assert.deepEqual(rxDetails(rx, false, 18), ['Length: 4.2 s', 'Loudest peak: -7 dBFS', 'Speech level: -31 dBFS']);
});

test('step slider labels and fractions', () => {
  assert.equal(hasSteps(35), true);
  assert.equal(hasSteps(0), false);
  assert.equal(hasSteps(undefined), false);
  assert.equal(stepText(9, 35, 12), '+12.0 dB');
  assert.equal(stepText(3, 35, -3.5), '-3.5 dB');
  assert.equal(stepText(9, 35, null), 'step 9 of 35');
  assert.equal(stepText(9, 35), 'step 9 of 35');
  assert.equal(stepText(0, 35, 0), '0.0 dB');
  // The slider never jumps: every step sent comes back as the same step.
  for (const steps of [35, 151, 7]) {
    for (let k = 0; k <= steps; k++) assert.equal(fractionStep(stepFraction(k, steps), steps), k);
  }
  assert.equal(stepFraction(99, 35), 1);
  assert.equal(stepFraction(-4, 35), 0);
  assert.equal(fractionStep(-1, 35), 0);
});

test('autosetText follows the run', () => {
  assert.equal(autosetText(null), '');
  assert.equal(autosetText({ state: 'idle' }), '');
  assert.equal(autosetText({ state: 'waiting' }), 'Key up and talk normally…');
  assert.equal(autosetText({ state: 'measuring' }), 'Listening… keep talking.');
  assert.equal(autosetText({ state: 'failed', error: 'That was too short.' }), 'That was too short.');
  assert.equal(autosetText({ state: 'done', result: { step: 9, steps: 35, db: 12, speechDb: -30, peakDb: -9 } }), 'Done: receive level set to +12.0 dB');
  assert.equal(autosetText({ state: 'done', result: { step: 9, steps: 35, db: null, speechDb: -30, peakDb: -9 } }), 'Done: receive level set to step 9 of 35');
  assert.equal(autosetRunning({ state: 'waiting' }), true);
  assert.equal(autosetRunning({ state: 'done' }), false);
  assert.equal(autosetRunning(null), false);
});
