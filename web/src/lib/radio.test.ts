import { test } from 'node:test';
import assert from 'node:assert/strict';
import { COS_SOURCES, CTCSS_SOURCES, hasSA818, pickPreset, pinError, radioBody, resolveInterface, presetName, detectedLabel } from './radio.ts';
import type { RadioInterface, RadioPreset, Station } from './types.ts';

const base = { audio: false, pttPin: 0, pttActiveLow: false, cos: 'none', cosActiveLow: false, ctcss: 'none', ctcssActiveLow: false, sa818: false };
const iface = (preset: string, o: Partial<RadioInterface>): RadioInterface => ({ ...base, preset, ...o });
const presets: RadioPreset[] = [
  { name: 'shari', title: 'SHARI', description: '', defaults: iface('shari', { audio: true, pttPin: 3, cos: 'gpio2', sa818: true }) },
  { name: 'uri', title: 'URI', description: '', defaults: iface('uri', { audio: true, pttPin: 3, cos: 'gpio2', cosActiveLow: true }) },
  { name: 'none', title: 'None', description: '', defaults: iface('none', {}) },
];
type R = Station['radio'];

test('automatic is the SHARI when one was found, else none', () => {
  assert.equal(presetName('', true), 'shari');
  assert.equal(presetName(undefined, false), 'none');
  assert.equal(presetName('uri', true), 'uri');
});

test('resolution applies overrides over the preset', () => {
  assert.deepEqual(resolveInterface({}, presets, true), presets[0].defaults);
  const r: R = { preset: 'uri', pttGpio: 4, pttActiveLow: true, cosSource: 'gpio1', cosActiveLow: false, ctcssSource: 'gpio3', sa818: true };
  const i = resolveInterface(r, presets, false)!;
  assert.equal(i.pttPin, 4); assert.equal(i.pttActiveLow, true); assert.equal(i.cos, 'gpio1'); assert.equal(i.cosActiveLow, false);
  assert.equal(i.ctcss, 'gpio3'); assert.equal(i.sa818, true);
  assert.equal(resolveInterface({ preset: 'bogus' }, presets, false), undefined);
});

test('the none preset ignores overrides', () => {
  const i = resolveInterface({ preset: 'none', pttGpio: 4 }, presets, true)!;
  assert.equal(i.pttPin, 0); assert.equal(i.audio, false);
});

test('SA818 fields only when the interface programs one', () => {
  assert.equal(hasSA818(resolveInterface({ preset: 'shari' }, presets, false)), true);
  assert.equal(hasSA818(resolveInterface({ preset: 'uri' }, presets, false)), false);
  assert.equal(hasSA818(resolveInterface({ preset: 'uri', sa818: true }, presets, false)), true);
  assert.equal(hasSA818(resolveInterface({ preset: 'none', sa818: true }, presets, false)), false);
  assert.equal(hasSA818(undefined), false);
});

test('picking a preset resets the overrides', () => {
  const r: R = { preset: 'uri', pttGpio: 4, cosSource: 'gpio1', sa818: true, tone: '100.0' };
  pickPreset(r, 'shari');
  assert.deepEqual(r, { preset: 'shari', tone: '100.0' });
});

test('only overrides that differ from the preset are saved', () => {
  const r: R = { preset: 'uri', pttGpio: 3, pttActiveLow: true, cosSource: 'gpio2', cosActiveLow: true, sa818: false, tone: '88.5' };
  assert.deepEqual(radioBody(r, presets, false), { preset: 'uri', pttActiveLow: true, tone: '88.5' });
  assert.deepEqual(radioBody({ pttGpio: 4 }, presets, true), { pttGpio: 4 });
  assert.deepEqual(radioBody({ preset: 'none', pttGpio: 4, sa818: true }, presets, true), { preset: 'none' });
  assert.deepEqual(radioBody({ pttGpio: 4 }, presets, false), {});
});

test('pin conflicts', () => {
  assert.equal(pinError(presets[0].defaults), '');
  assert.match(pinError(iface('x', { audio: true, pttPin: 2, cos: 'gpio2' })), /PTT pin/);
  assert.match(pinError(iface('x', { audio: true, pttPin: 4, ctcss: 'gpio4' })), /tone/);
  assert.match(pinError(iface('x', { audio: true, pttPin: 3, cos: 'gpio1', ctcss: 'gpio1' })), /squelch and tone/);
  assert.equal(pinError(iface('none', {})), '');
});

test('detected label', () => {
  assert.equal(detectedLabel({ chip: 'CM108B', usbId: '0d8c:0012' }), 'CM108B (0d8c:0012)');
  assert.equal(detectedLabel({}), '');
});

test('tone input sources have their own wording and no always-open', () => {
  assert.equal(CTCSS_SOURCES[0]!.value, 'none');
  assert.equal(CTCSS_SOURCES[0]!.label, 'None (no tone input)');
  assert.equal(CTCSS_SOURCES.some((o) => o.value === 'always'), false);
  assert.deepEqual(CTCSS_SOURCES.slice(1).map((o) => o.value), ['gpio1', 'gpio2', 'gpio3', 'gpio4']);
  assert.equal(COS_SOURCES[0]!.label, 'None (squelch is not read)');
});
