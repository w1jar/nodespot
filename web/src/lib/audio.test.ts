import test from 'node:test';
import assert from 'node:assert/strict';
import { audioBody, audioDirty, audioError, effectivePL, fieldError, filterOn, programmedText, withKeys, AGC_DEFAULTS, AGC_FIELDS, AUDIO_ADV_KEYS, RADIO_ADV_KEYS, SA818_FILTER_DEFAULTS, DELAY_FIELDS, GAIN_FIELDS, TIMER_FIELDS, ID_FIELDS } from './audio.ts';
import type { Station } from './types';

const base = (): Station => ({ callsign: 'N0CALL', radio: { frequencyMHz: 146.52 }, id: { enabled: true, wpm: 20, extra: 1 }, advanced: { hangMs: 700, md380: '127.0.0.1:2470', modeAgc: false }, dmrId: 1234567 });

test('limits match the node', () => {
  const f = (k: string) => [...AGC_FIELDS, ...TIMER_FIELDS, ...ID_FIELDS].find((x) => x.key === k)!;
  assert.deepEqual([f('rxAgcTargetDb').min, f('rxAgcTargetDb').max, f('rxAgcTargetDb').def], [-40, -6, -22]);
  assert.deepEqual([f('txAgcTargetDb').min, f('txAgcTargetDb').max, f('txAgcTargetDb').def], [-40, -6, -22]);
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

test('flat audio and PL in the body', () => {
  const b = base();
  const out = audioBody(b, { ...b.advanced, rxGainDb: 0, txGainDb: -3.5, flatAudio: true, plFilter: false, rxOnDelayMs: 0, txOffDelayMs: 250 }, b.id);
  assert.equal(out.advanced.rxGainDb, undefined);
  assert.equal(out.advanced.txGainDb, -3.5);
  assert.equal(out.advanced.flatAudio, true);
  assert.equal(audioBody(b, { ...b.advanced, flatAudio: false }, b.id).advanced.flatAudio, undefined); // off is the default
  assert.equal(out.advanced.plFilter, false); // an explicit off is kept: unset means automatic
  assert.equal(out.advanced.txOffDelayMs, 250);
  assert.equal(audioBody(b, { ...b.advanced, plFilter: undefined }, b.id).advanced.plFilter, undefined);
});

test('key-up delay: empty is the default, 0 is off and kept', () => {
  const f = DELAY_FIELDS.find((x) => x.key === 'txOnDelayMs')!;
  assert.deepEqual([f.min, f.max, f.def], [0, 1000, 200]);
  assert.equal(audioError({ txOnDelayMs: 1000 }, {}), '');
  assert.notEqual(audioError({ txOnDelayMs: 1001 }, {}), '');
  assert.notEqual(audioError({ txOnDelayMs: -10 }, {}), '');
  const b = base();
  assert.equal(audioBody(b, { ...b.advanced, txOnDelayMs: 0 }, b.id).advanced.txOnDelayMs, 0);
  assert.equal(audioBody(b, { ...b.advanced, txOnDelayMs: '300' as unknown as number }, b.id).advanced.txOnDelayMs, 300);
  assert.equal('txOnDelayMs' in audioBody(b, { ...b.advanced, txOnDelayMs: undefined }, b.id).advanced, false);
  assert.equal(audioDirty(b, { ...b.advanced, txOnDelayMs: 0 }, b.id), true);
});

test('effective PL', () => {
  assert.equal(effectivePL({}, false), true);
  assert.equal(effectivePL({}, true), false);
  assert.equal(effectivePL({ plFilter: true }, true), true);
  assert.equal(effectivePL({ plFilter: false }, false), false);
});

test('SA818 filter defaults: emphasis off, high-pass and low-pass on', () => {
  assert.deepEqual(SA818_FILTER_DEFAULTS, { emphasis: false, highPass: true, lowPass: true });
  assert.equal(filterOn(undefined, 'emphasis'), false);
  assert.equal(filterOn({}, 'highPass'), true);
  assert.equal(filterOn({ emphasis: true, lowPass: false }, 'emphasis'), true);
  assert.equal(filterOn({ emphasis: true, lowPass: false }, 'lowPass'), false);
});

test('SA818 filters in the body', () => {
  const b = base();
  assert.equal(audioBody(b, b.advanced, b.id, { emphasis: false, highPass: true, lowPass: true }).radio.sa818Filter, undefined);
  assert.deepEqual(audioBody(b, b.advanced, b.id, { emphasis: true, highPass: true }).radio.sa818Filter, { emphasis: true });
  assert.deepEqual(audioBody(b, b.advanced, b.id, { emphasis: false, highPass: false }).radio.sa818Filter, { highPass: false });
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
  const out = audioBody(b, { ...b.advanced, hangMs: '' as unknown as number, agcMaxDb: 20, modeAgc: true, radioAgc: false }, { ...b.id, enabled: true, wpm: null as unknown as number, voiceFile: ' /a/id.wav ' });
  assert.equal(out.callsign, 'N0CALL');
  assert.equal(out.dmrId, 1234567);
  assert.deepEqual(out.advanced, { md380: '127.0.0.1:2470', agcMaxDb: 20, radioAgc: false });
  assert.deepEqual(out.id, { extra: 1, voiceFile: '/a/id.wav' });
  const t = audioBody(b, { rxAgcTargetDb: '-30' as unknown as number, txAgcTargetDb: -18 }, {}).advanced;
  assert.deepEqual([t.rxAgcTargetDb, t.txAgcTargetDb], [-30, -18]);
});

test('AGC: both directions on by default, separate targets', () => {
  assert.deepEqual(AGC_DEFAULTS, { modeAgc: true, radioAgc: true, flatAudio: false });
  assert.equal(audioError({ rxAgcTargetDb: -41 }, {}) !== '', true);
  assert.equal(audioError({ txAgcTargetDb: -5 }, {}) !== '', true);
  assert.equal(audioError({ rxAgcTargetDb: -40, txAgcTargetDb: -6 }, {}), '');
  const b = base();
  assert.equal(audioBody(b, { radioAgc: true, modeAgc: true }, {}).advanced.radioAgc, undefined);
});

test('each tab saves only its own keys', () => {
  assert.ok(RADIO_ADV_KEYS.includes('hangMs') && RADIO_ADV_KEYS.includes('flatAudio') && RADIO_ADV_KEYS.includes('plFilter'));
  assert.ok(AUDIO_ADV_KEYS.includes('rxAgcTargetDb') && AUDIO_ADV_KEYS.includes('radioAgc') && AUDIO_ADV_KEYS.includes('rxGainDb'));
  assert.equal(RADIO_ADV_KEYS.some((k) => AUDIO_ADV_KEYS.includes(k)), false);
  const saved = { hangMs: 900, rxGainDb: 3, md380: 'x' };
  assert.deepEqual(withKeys(saved, { hangMs: 700, rxGainDb: -1 }, RADIO_ADV_KEYS), { hangMs: 700, rxGainDb: 3, md380: 'x' });
  assert.deepEqual(withKeys(saved, { hangMs: 700 }, AUDIO_ADV_KEYS), { hangMs: 900, md380: 'x' });
});

test('dirty', () => {
  const b = base();
  assert.equal(audioDirty(b, { ...b.advanced }, { ...b.id }), false);
  assert.equal(audioDirty(b, { ...b.advanced, hangMs: 700 }, { ...b.id, enabled: undefined }), false);
  assert.equal(audioDirty(b, { ...b.advanced, hangMs: 800 }, b.id), true);
  assert.equal(audioDirty(b, b.advanced, { ...b.id, enabled: false }), true);
  assert.equal(audioDirty(b, { ...b.advanced, modeAgc: undefined }, b.id), true);
});
