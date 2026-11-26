import test from 'node:test';
import assert from 'node:assert/strict';
import { audioBody, audioDirty, audioError, effectivePL, fieldError, programmedText, AGC_FIELDS, DELAY_FIELDS, GAIN_FIELDS, TIMER_FIELDS, ID_FIELDS } from './audio.ts';
import type { Station } from './types';

const base = (): Station => ({ callsign: 'N0CALL', radio: { frequencyMHz: 146.52 }, id: { enabled: true, wpm: 20, extra: 1 }, advanced: { hangMs: 700, md380: '127.0.0.1:2470', modeAgc: false }, dmrId: 1234567 });

test('limits match the node', () => {
  const f = (k: string) => [...AGC_FIELDS, ...TIMER_FIELDS, ...ID_FIELDS].find((x) => x.key === k)!;
  assert.deepEqual([f('agcTargetDb').min, f('agcTargetDb').max, f('agcTargetDb').def], [-40, -6, -22]);
  assert.deepEqual([f('agcMaxDb').max, f('agcMaxDb').def], [30, 18]);
  assert.deepEqual([f('hangMs').max, f('hangMs').def], [10000, 500]);
  assert.deepEqual([f('txTimeoutS').max, f('txTimeoutS').def], [1800, 180]);
  assert.deepEqual([f('intervalS').max, f('wpm').min, f('wpm').max, f('pitchHz').min, f('levelDb').min], [600, 5, 60, 200, -60]);
});

test('new limits match the node', () => {
  const f = (k: string) => [...GAIN_FIELDS, ...DELAY_FIELDS].find((x) => x.key === k)!;
  assert.deepEqual([f('rxGainDb').min, f('rxGainDb').max, f('rxGainDb').def], [-20, 20, 0]);
  assert.deepEqual([f('txGainDb').min, f('txGainDb').max], [-20, 20]);
  assert.deepEqual([f('rxOnDelayMs').min, f('rxOnDelayMs').max, f('txOffDelayMs').max], [0, 1000, 2000]);
});

test('gain and delay checks', () => {
  assert.equal(audioError({ rxGainDb: -20, txGainDb: 20.5 - 0.5, rxOnDelayMs: 1000, txOffDelayMs: 2000 }, {}), '');
  assert.notEqual(audioError({ rxGainDb: 20.5 }, {}), '');
  assert.notEqual(audioError({ txGainDb: -21 }, {}), '');
  assert.notEqual(audioError({ rxOnDelayMs: 1001 }, {}), '');
  assert.notEqual(audioError({ txOffDelayMs: 2001 }, {}), '');
  assert.notEqual(audioError({ rxOnDelayMs: -1 }, {}), '');
});

test('emphasis and PL in the body', () => {
  const b = base();
  const out = audioBody(b, { ...b.advanced, rxGainDb: 0, txGainDb: -3.5, preEmphasis: true, deEmphasis: false, plFilter: false, rxOnDelayMs: 0, txOffDelayMs: 250 }, b.id);
  assert.equal(out.advanced.rxGainDb, undefined);
  assert.equal(out.advanced.txGainDb, -3.5);
  assert.equal(out.advanced.preEmphasis, true);
  assert.equal(out.advanced.deEmphasis, undefined);
  assert.equal(out.advanced.plFilter, false); // an explicit off is kept: unset means automatic
  assert.equal(out.advanced.txOffDelayMs, 250);
  assert.equal(audioBody(b, { ...b.advanced, plFilter: undefined }, b.id).advanced.plFilter, undefined);
});

test('effective PL', () => {
  assert.equal(effectivePL({}, false), true);
  assert.equal(effectivePL({}, true), false);
  assert.equal(effectivePL({ plFilter: true }, true), true);
  assert.equal(effectivePL({ plFilter: false }, false), false);
});

test('SA818 filters in the body', () => {
  const b = base();
  assert.equal(audioBody(b, b.advanced, b.id, { emphasis: true, highPass: true, lowPass: true }).radio.sa818Filter, undefined);
  assert.deepEqual(audioBody(b, b.advanced, b.id, { emphasis: false, highPass: true }).radio.sa818Filter, { emphasis: false });
  assert.equal(audioBody(b, b.advanced, b.id).radio.sa818Filter, undefined); // not given: radio untouched
  const s: Station = { ...b, radio: { ...b.radio, sa818Filter: { lowPass: false } } };
  assert.equal(audioDirty(s, s.advanced, s.id), false);
  assert.equal(audioDirty(s, s.advanced, s.id, { lowPass: false }), false);
  assert.equal(audioDirty(s, s.advanced, s.id, { lowPass: true }), true);
  assert.equal(audioDirty(b, b.advanced, b.id, { highPass: false }), true);
});

test('programmed filters text', () => {
  assert.match(programmedText(undefined), /unknown/i);
  assert.match(programmedText({ emphasis: true, highPass: false, lowPass: true }), /emphasis on, high-pass off, low-pass on/);
});

test('field checks', () => {
  const t = AGC_FIELDS[0]!;
  assert.equal(fieldError(t, undefined), '');
  assert.equal(fieldError(t, null), '');
  assert.equal(fieldError(t, ''), '');
  assert.equal(fieldError(t, -40), '');
  assert.equal(fieldError(t, -6), '');
  assert.notEqual(fieldError(t, -5), '');
  assert.notEqual(fieldError(t, -41), '');
  assert.notEqual(fieldError(t, 0), '');
  assert.notEqual(fieldError(t, -22.5), '');
  assert.notEqual(fieldError(t, 'abc'), '');
});

test('card check', () => {
  assert.equal(audioError({}, {}), '');
  assert.notEqual(audioError({ txTimeoutS: 1801 }, {}), '');
  assert.notEqual(audioError({}, { wpm: 4 }), '');
  assert.notEqual(audioError({}, { voiceFile: 'id.wav' }), '');
  assert.equal(audioError({}, { voiceFile: '/var/lib/nodespot/id.wav' }), '');
});

test('body keeps other fields and leaves defaults out', () => {
  const b = base();
  const out = audioBody(b, { ...b.advanced, hangMs: '' as unknown as number, agcMaxDb: 20, modeAgc: true, radioAgc: true }, { ...b.id, enabled: true, wpm: null as unknown as number, voiceFile: ' /a/id.wav ' });
  assert.equal(out.callsign, 'N0CALL');
  assert.equal(out.dmrId, 1234567);
  assert.deepEqual(out.advanced, { md380: '127.0.0.1:2470', agcMaxDb: 20, radioAgc: true });
  assert.deepEqual(out.id, { extra: 1, voiceFile: '/a/id.wav' });
  assert.equal(audioBody(b, { ...b.advanced, courtesyTone: false }, b.id).advanced.courtesyTone, false);
  assert.equal(audioBody(b, { agcTargetDb: '-30' as unknown as number }, {}).advanced.agcTargetDb, -30);
});

test('dirty', () => {
  const b = base();
  assert.equal(audioDirty(b, { ...b.advanced }, { ...b.id }), false);
  assert.equal(audioDirty(b, { ...b.advanced, hangMs: 700 }, { ...b.id, enabled: undefined }), false);
  assert.equal(audioDirty(b, { ...b.advanced, hangMs: 800 }, b.id), true);
  assert.equal(audioDirty(b, b.advanced, { ...b.id, enabled: false }), true);
  assert.equal(audioDirty(b, { ...b.advanced, modeAgc: undefined }, b.id), true);
});
