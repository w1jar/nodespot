import { test } from 'node:test';
import assert from 'node:assert/strict';
import { groups, visibleGroups, defaultFor } from './sections.ts';
import { MOVED, OLD_IDS, modesMoved } from '../../lib/sysnav.ts';

const tabIds = (id: string) => groups.find((g) => g.id === id)?.tabs?.map((t) => t.id);

test('six groups in the menu order; the modes live on the Modes page', () => {
  assert.deepEqual(groups.map((g) => g.label), ['Station', 'Access', 'System', 'Maintenance', 'About', 'Account']);
  assert.deepEqual(groups.map((g) => g.id), ['station', 'access', 'system', 'maintenance', 'about', 'account']);
});

test('group and tab ids are unique kebab-case', () => {
  const ids = [...groups.map((g) => g.id), ...groups.flatMap((g) => (g.tabs ?? []).map((t) => g.id + '/' + t.id))];
  assert.equal(new Set(ids).size, ids.length);
  for (const g of groups) {
    assert.match(g.id, /^[a-z0-9]+(-[a-z0-9]+)*$/);
    for (const t of g.tabs ?? []) assert.match(t.id, /^[a-z0-9]+(-[a-z0-9]+)*$/);
  }
});

test('a group has tabs or pages, never both', () => {
  for (const g of groups) assert.equal(!!g.tabs?.length !== !!g.pages?.length, true, g.id);
});

test('tabs of each tabbed group, in order', () => {
  assert.deepEqual(tabIds('station'), ['station', 'radio', 'lookup']);
  assert.deepEqual(tabIds('system'), ['overview', 'updates', 'network', 'wifi', 'web-port', 'node', 'terminal']);
  assert.deepEqual(tabIds('maintenance'), ['backup', 'logs', 'diagnostics', 'audit']);
});

test('there is no Settings -> Modes group and no per-mode page', () => {
  assert.equal(groups.some((g) => g.id === 'modes'), false);
  for (const g of groups) for (const t of g.tabs ?? []) assert.equal(t.props, undefined);
});

test('no Asterisk, ASL services or bridge pages are left', () => {
  const all = groups.flatMap((g) => (g.tabs ?? []).map((t) => g.id + '/' + t.id));
  for (const id of all) assert.doesNotMatch(id, /asterisk|asl-services|node-list|bridge|digital/);
});

test('an admin sees every group, anyone else only About and Account', () => {
  assert.equal(visibleGroups(groups, true).length, 6);
  assert.deepEqual(visibleGroups(groups, false).map((g) => g.id), ['about', 'account']);
});

test('the default group is one the role can open', () => {
  assert.equal(defaultFor(true), 'station');
  assert.ok(visibleGroups(groups, true).some((g) => g.id === defaultFor(true)));
  assert.ok(visibleGroups(groups, false).some((g) => g.id === defaultFor(false)));
});

test('every redirect and old id lands on a real group and tab', () => {
  const real = (old: string) => {
    const to = modesMoved(old) ?? old;
    if (to.startsWith('/modes')) return; // the Modes page
    const [, , gid, tid] = to.split('/');
    const g = groups.find((x) => x.id === gid);
    assert.ok(g, to);
    if (tid) assert.ok(g.tabs?.some((t) => t.id === tid), to);
    else assert.ok(!g.tabs || g.tabs.length > 0);
  };
  for (const to of Object.values(MOVED)) real(to);
  for (const [g, t] of Object.values(OLD_IDS)) real('/settings/' + g + (t ? '/' + t : ''));
});
