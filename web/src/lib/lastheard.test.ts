import test from 'node:test';
import assert from 'node:assert/strict';
import { callItems, collapseHeard, heardItems, mergeHeard, targetCell } from './lastheard.ts';
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

test('callItems: ASL rows show the node description', () => {
  const [r] = callItems([c({ desc: 'Hub (fake)', node: '1000' })]);
  assert.equal(r.mode, 'ASL');
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

test('mergeHeard: the same ASL transmission from both sources is one row, the resolved one', () => {
  const heard = [h({ mode: 'allstar', callsign: '1997', name: 'Fav Hub', target: '1997', start: '2026-11-20T10:05:01Z', end: '2026-11-20T10:05:09Z' })];
  const recent = [c({ call: 'N1CALL', target: '1997', desc: 'Example Hub', node: '1997', start: '2026-11-20T10:05:00Z', end: '2026-11-20T10:05:09Z' })];
  const rows = mergeHeard(recent, heard);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].call, 'N1CALL');
  assert.equal(rows[0].person, 'Example Hub');
  // Far apart in time: two transmissions.
  const later = [h({ mode: 'allstar', callsign: '1997', target: '1997', start: '2026-11-20T10:09:00Z', end: '2026-11-20T10:09:05Z' })];
  assert.equal(mergeHeard(recent, later).length, 2);
});

test('collapseHeard: consecutive repeats become one row with a count; another station in between breaks the run', () => {
  const rows = mergeHeard(undefined, [
    h({ mode: 'allstar', callsign: 'N1CALL', target: '1997', start: '2026-11-20T10:00:00Z', end: '2026-11-20T10:00:05Z' }),
    h({ mode: 'allstar', callsign: 'N1CALL', target: '1997', start: '2026-11-20T10:01:00Z', end: '2026-11-20T10:01:05Z' }),
    h({ mode: 'allstar', callsign: 'N1CALL', target: '1997', start: '2026-11-20T10:02:00Z', end: '2026-11-20T10:02:05Z' }),
    h({ mode: 'allstar', callsign: 'N2CALL', target: '1997', start: '2026-11-20T10:03:00Z', end: '2026-11-20T10:03:05Z' }),
    h({ mode: 'allstar', callsign: 'N1CALL', target: '1997', start: '2026-11-20T10:04:00Z', end: '2026-11-20T10:04:05Z' }),
  ]);
  assert.deepEqual(rows.map((r) => [r.call, r.count]), [['N1CALL', undefined], ['N2CALL', undefined], ['N1CALL', 3]]);
  assert.equal(rows[2].at, Date.parse('2026-11-20T10:02:05Z'));
  const one = [rows[0]];
  assert.equal(collapseHeard(one)[0].count, undefined);
  assert.equal(collapseHeard([rows[0], { ...rows[0], target: 'Node 2000' }]).length, 2);
});
