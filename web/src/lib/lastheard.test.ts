import test from 'node:test';
import assert from 'node:assert/strict';
import { callItems, heardItems, mergeHeard, pubHeardItems, targetCell } from './lastheard.ts';
import type { Heard, NowCall } from './types';

const h = (o: Partial<Heard> = {}): Heard => ({ mode: 'bm', callsign: 'N0CALL', target: '91', start: '2026-11-20T10:00:00Z', end: '2026-11-20T10:00:05Z', ...o });
const c = (o: Partial<NowCall> = {}): NowCall => ({ id: 'a', kind: 'allstar', call: 'N1CALL', target: '1000', start: '2026-11-20T10:05:00Z', end: '2026-11-20T10:05:09Z', active: false, ...o });

test('targetCell formats by mode', () => {
  assert.equal(targetCell('bm', '91'), 'TG 91');
  assert.equal(targetCell('allstar', '1000'), 'Node 1000');
  assert.equal(targetCell('ysf', 'YSF-TEST'), 'YSF-TEST');
  assert.equal(targetCell('bm', undefined), '');
});

test('heardItems: one row per heard entry with the mode title and links', () => {
  const [r] = heardItems([h({ id: 1234567, name: 'Pat Example', place: 'Springfield, XX' })]);
  assert.equal(r.call, 'N0CALL');
  assert.equal(r.mode, 'BrandMeister');
  assert.equal(r.target, 'TG 91');
  assert.equal(r.person, 'Pat Example · Springfield, XX');
  assert.equal(r.qrz, 'https://www.qrz.com/db/N0CALL');
  assert.notEqual(r.radioid, '');
  assert.equal(heardItems([h({ callsign: '', id: 1234567 })])[0].call, 'ID 1234567');
  assert.equal(heardItems([h({ callsign: '' })])[0].call, 'Unknown');
});

test('callItems: AllStar rows show the node description', () => {
  const [r] = callItems([c({ desc: 'Hub (fake)', node: '1000' })]);
  assert.equal(r.mode, 'AllStar');
  assert.equal(r.person, 'Hub (fake)');
  assert.equal(r.target, 'Node 1000');
});

test('mergeHeard: newest first, the same call from both sources once, limited', () => {
  const heard = [h(), h({ callsign: 'N1CALL', mode: 'allstar', target: '1000', start: '2026-11-20T10:05:00Z', end: '2026-11-20T10:05:09Z' })];
  const rows = mergeHeard([c()], heard);
  assert.equal(rows.length, 2);
  assert.equal(rows[0].call, 'N1CALL');
  assert.equal(mergeHeard([c()], heard, 1).length, 1);
  assert.deepEqual(mergeHeard(undefined, undefined), []);
});

test('pubHeardItems: nothing unless the page shows heard calls; never a radioid link', () => {
  const snap = { showHeard: true, heard: [{ mode: 'bm', callsign: 'N0CALL', target: '91', at: 100, duration: 3 }, { mode: 'ysf', target: 'YSF-TEST', at: 200, duration: 2 }] };
  const rows = pubHeardItems(snap);
  assert.deepEqual(rows.map((r) => r.call), ['YSF caller', 'N0CALL']);
  assert.ok(rows.every((r) => r.radioid === ''));
  assert.deepEqual(pubHeardItems({ ...snap, showHeard: false }), []);
  assert.deepEqual(pubHeardItems(null), []);
});

test('callItems: the place is "City, ST" for US callers and "City, Country" otherwise, else the location', () => {
  const [us] = callItems([c({ kind: 'bm', call: 'N0CALL', first: 'Pat', city: 'Springfield', state: 'Illinois', country: 'United States' })]);
  assert.match(us.person, /Springfield, IL/);
  const [intl] = callItems([c({ kind: 'bm', call: 'N1CALL', first: 'Sam', city: 'Exampleton', state: 'Ontario', country: 'Canada' })]);
  assert.match(intl.person, /Exampleton, Canada/);
  const [loc] = callItems([c({ kind: 'ysf', call: 'N1CALL', first: 'Sam', location: 'Sampleburg, ST' })]);
  assert.match(loc.person, /Sampleburg, ST/);
  const [as] = callItems([c({ kind: 'allstar', desc: 'Hub (fake)', city: 'Springfield', state: 'Illinois' })]);
  assert.equal(as.person, 'Hub (fake)');
});
