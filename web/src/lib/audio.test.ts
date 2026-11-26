import test from 'node:test';
import assert from 'node:assert/strict';
import { audioBody, audioDirty, audioError, fieldError, AGC_FIELDS, TIMER_FIELDS, ID_FIELDS } from './audio.ts';
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
