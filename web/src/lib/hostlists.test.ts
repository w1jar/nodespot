import test from 'node:test';
import assert from 'node:assert/strict';
import { masterOptions, masterSearch, masterHint } from './hostlists.ts';
import { MASTER_OTHER, splitAddress, masterChoice, masterAddress, masterPick, masterLabel, emptyListNote, masterListNote, listRows, whenText, refreshWait } from './hostlists.ts';
import type { HostItem, HostListsStatus } from './types';

const bm: HostItem[] = [
  { value: 'bm1.example.net', name: '2001 – Test Land', address: 'bm1.example.net', port: 0 },
  { value: 'bm2.example.net', name: '3104 – United States', address: 'bm2.example.net', port: 0 },
];
const other: HostItem[] = [
  { value: '192.0.2.10', name: 'Test master', address: '192.0.2.10', port: 62031, password: 'passw0rd' },
  { value: 'dmr2.example.net', name: 'Second master', address: 'dmr2.example.net', port: 55555, password: 'secret' },
];

test('addresses split into host and port', () => {
  assert.deepEqual(splitAddress('a.example.net'), { host: 'a.example.net', port: 0 });
  assert.deepEqual(splitAddress(' a.example.net:54007 '), { host: 'a.example.net', port: 54007 });
  assert.deepEqual(splitAddress('192.0.2.1:62031'), { host: '192.0.2.1', port: 62031 });
});

test('a saved BrandMeister address selects its entry, else Other, nothing saved is empty', () => {
  assert.equal(masterChoice('bm', '', bm), '');
  assert.equal(masterChoice('bm', 'bm2.example.net', bm), 'bm2.example.net');
  assert.equal(masterChoice('bm', 'BM2.example.net:54006', bm), 'bm2.example.net');
  assert.equal(masterChoice('bm', 'bm2.example.net:54007', bm), MASTER_OTHER);
  assert.equal(masterChoice('bm', 'bm9.example.net', bm), MASTER_OTHER);
  assert.equal(masterChoice('bm', 'bm9.example.net', []), MASTER_OTHER);
});

test('a saved DMR address matches host and port (62031 when none is given)', () => {
  assert.equal(masterChoice('dmr-other', '192.0.2.10:62031', other), '192.0.2.10');
  assert.equal(masterChoice('dmr-other', '192.0.2.10', other), '192.0.2.10');
  assert.equal(masterChoice('dmr-other', 'dmr2.example.net:55555', other), 'dmr2.example.net');
  assert.equal(masterChoice('dmr-other', 'dmr2.example.net', other), MASTER_OTHER);
  assert.equal(masterChoice('dmr-other', 'dmr2.example.net:62031', other), MASTER_OTHER);
});

test('a BrandMeister pick is the host alone, a DMR pick is host:port', () => {
  assert.equal(masterAddress('bm', bm[0]), 'bm1.example.net');
  assert.equal(masterAddress('dmr-other', other[1]), 'dmr2.example.net:55555');
  assert.equal(masterAddress('dmr-other', { ...other[0], port: 0 }), '192.0.2.10');
});

test('a DMR pick fills the list password only when the field is empty and none is saved', () => {
  assert.deepEqual(masterPick('dmr-other', other[1], { address: '', password: '' }, false), { address: 'dmr2.example.net:55555', password: 'secret' });
  assert.deepEqual(masterPick('dmr-other', other[1], { address: '', password: 'mine' }, false), { address: 'dmr2.example.net:55555' });
  assert.deepEqual(masterPick('dmr-other', other[1], { address: '', password: '' }, true), { address: 'dmr2.example.net:55555' });
  assert.deepEqual(masterPick('dmr-other', other[1], { address: '' }, false), { address: 'dmr2.example.net:55555' });
  assert.deepEqual(masterPick('bm', { ...bm[0], password: 'x' }, { address: '', password: '' }, false), { address: 'bm1.example.net' });
});

test('labels', () => {
  assert.equal(masterLabel('bm', bm[1]), '3104 – United States');
  assert.equal(masterLabel('dmr-other', other[0]), 'Test master (192.0.2.10:62031)');
  assert.equal(masterLabel('dmr-other', { value: 'h.example.net', name: 'h.example.net', address: 'h.example.net', port: 0 }), 'h.example.net');
});

test('the notes point to Settings > Station > Host lists', () => {
  assert.equal(emptyListNote('ysf', 'YSF'), "The YSF reflector list hasn't downloaded yet. See Settings → Station → Host lists.");
  assert.match(emptyListNote('p25', 'P25'), /^The P25 talkgroup list/);
  assert.match(masterListNote('bm'), /BrandMeister master list hasn't downloaded yet/);
});

const st: HostListsStatus = {
  lists: { dmr: { entries: 4, updated: '2026-11-26T06:12:00Z' }, ysf: { entries: 0, error: 'download failed: HTTP 503' } },
  nodelist: { count: 100, updated: null, source: 'none', error: 'no nodelist URL configured' },
};

test('rows: AllStar nodes first, then the five lists; a missing list is empty', () => {
  const rows = listRows(st);
  assert.deepEqual(rows.map((r) => r.id), ['nodelist', 'dmr', 'ysf', 'p25', 'nxdn', 'm17']);
  assert.equal(rows[0].error, 'no nodelist URL configured');
  assert.equal(rows[1].count, 4);
  assert.equal(rows[2].error, 'download failed: HTTP 503');
  assert.equal(rows[3].count, 0);
  assert.deepEqual(listRows(null), []);
  assert.equal(listRows({ lists: {} }).length, 5);
});

test('update time and the wait for the next manual refresh', () => {
  assert.equal(whenText(''), 'never');
  assert.equal(whenText('nonsense'), 'never');
  assert.notEqual(whenText('2026-11-26T06:12:00Z'), 'never');
  const now = Date.parse('2026-11-26T12:00:00Z');
  assert.equal(refreshWait(undefined, now), 0);
  assert.equal(refreshWait('2026-11-26T12:10:00Z', now), 600);
  assert.equal(refreshWait('2026-11-26T11:00:00Z', now), 0);
});

test('master combobox entries write the saved address format and filter by name, host or country', () => {
  const ob = masterOptions('bm', bm);
  assert.deepEqual(ob.map((o) => o.value), ['bm1.example.net', 'bm2.example.net']);
  assert.equal(ob[0].name, '2001 – Test Land');
  assert.equal(ob[0].detail, 'bm1.example.net');
  const od = masterOptions('dmr-other', other);
  assert.deepEqual(od.map((o) => o.value), ['192.0.2.10:62031', 'dmr2.example.net:55555']);
  assert.equal(od[0].detail, undefined, 'the label already shows the host');
  assert.deepEqual(masterSearch(ob, 'united').map((o) => o.value), ['bm2.example.net'], 'country');
  assert.deepEqual(masterSearch(ob, 'BM1').map((o) => o.value), ['bm1.example.net'], 'host');
  assert.deepEqual(masterSearch(ob, '3104 states').map((o) => o.value), ['bm2.example.net'], 'every word');
  assert.deepEqual(masterSearch(ob, '').length, 2);
  assert.deepEqual(masterSearch(od, 'second').map((o) => o.value), ['dmr2.example.net:55555']);
  assert.deepEqual(masterSearch(ob, 'nowhere'), []);
  assert.deepEqual(masterSearch(ob, '', 1).length, 1);
});

test('the master hint names the list entry, or says a typed address is used as is', () => {
  assert.equal(masterHint('bm', '', bm), '');
  assert.equal(masterHint('bm', 'bm2.example.net', bm), 'From the list: 3104 – United States');
  assert.equal(masterHint('bm', 'any.example.net:54007', bm), 'Not in the list: used as typed.');
  assert.equal(masterHint('dmr-other', 'dmr2.example.net:55555', other), 'From the list: Second master (dmr2.example.net:55555)');
});
