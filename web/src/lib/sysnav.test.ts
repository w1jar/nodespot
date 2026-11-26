import { test } from 'node:test';
import assert from 'node:assert/strict';
import { movedPath, MOVED, OLD_IDS, isSettingsPath, settingsRoute, clampLines, auditSummary } from './sysnav.ts';

test('movedPath redirects every legacy route', () => {
  assert.equal(movedPath('/updates'), '/settings/system/updates');
  assert.equal(movedPath('/tune'), '/settings/station/radio');
  assert.equal(movedPath('/digital'), '/modes');
  assert.equal(movedPath('/backup'), '/settings/maintenance/backup');
  assert.equal(movedPath('/system'), '/settings/system/overview');
  assert.equal(movedPath('/system/'), '/settings/system/overview');
});

test('movedPath maps each old System section to its tab', () => {
  assert.equal(movedPath('/system/updates'), '/settings/system/updates');
  assert.equal(movedPath('/system/wifi'), '/settings/system/wifi');
  assert.equal(movedPath('/system/logs'), '/settings/maintenance/logs');
  assert.equal(movedPath('/system/diagnostics'), '/settings/maintenance/diagnostics');
  assert.equal(movedPath('/system/audit'), '/settings/maintenance/audit');
  assert.equal(movedPath('/system/nope'), '/settings/system/overview');
});

test('movedPath maps every flat settings id of the first Settings layout', () => {
  assert.equal(Object.keys(OLD_IDS).length, 21);
  assert.equal(movedPath('/settings/audio-levels'), '/settings/station/radio');
  for (const id of ['dv-engine', 'dv-identity', 'dv-bridge', 'dv-audio']) assert.equal(movedPath('/settings/' + id), '/modes');
  assert.equal(movedPath('/settings/dv-user-list'), '/settings/station/lookup');
  assert.equal(movedPath('/settings/users'), '/settings/access');
  assert.equal(movedPath('/settings/public'), '/settings/access');
  assert.equal(movedPath('/settings/web-port'), '/settings/system/web-port');
  assert.equal(movedPath('/settings/backup/'), '/settings/maintenance/backup');
  assert.equal(movedPath('/settings/audit'), '/settings/maintenance/audit');
});

test('movedPath leaves current routes alone', () => {
  for (const p of ['/', '/settings', '/settings/about', '/settings/account', '/settings/system', '/settings/system/updates', '/settings/access', '/modes', '/schedules', '/systematic']) assert.equal(movedPath(p), null);
});

test('every redirect target is a settings route', () => {
  for (const to of Object.values(MOVED)) assert.equal(isSettingsPath(to), true);
});

test('isSettingsPath', () => {
  assert.equal(isSettingsPath('/settings'), true);
  assert.equal(isSettingsPath('/settings/system/logs'), true);
  assert.equal(isSettingsPath('/settingsx'), false);
  assert.equal(isSettingsPath('/system'), false);
});

const G = [
  { id: 'allstarlink', tabs: ['asterisk', 'audio-levels', 'node-list'] },
  { id: 'access', tabs: [] },
  { id: 'system', tabs: ['overview', 'updates'] },
  { id: 'about', tabs: [] },
  { id: 'account', tabs: [] },
];
const OTHER = G.filter((g) => g.id === 'about' || g.id === 'account');

test('settingsRoute: group and tab', () => {
  assert.deepEqual(settingsRoute('/settings/system/updates', G, 'system', true), { group: 'system', tab: 'updates' });
  assert.deepEqual(settingsRoute('/settings/system/', G, 'system', true), { group: 'system', tab: 'overview' });
  assert.deepEqual(settingsRoute('/settings/allstarlink', G, 'system', true), { group: 'allstarlink', tab: 'asterisk' });
  assert.deepEqual(settingsRoute('/settings/allstarlink/audio-levels', G, 'system', true), { group: 'allstarlink', tab: 'audio-levels' });
  assert.deepEqual(settingsRoute('/settings/access', G, 'system', true), { group: 'access', tab: null });
});

test('settingsRoute: an unknown tab shows the first tab, a tab on a single page is ignored', () => {
  assert.deepEqual(settingsRoute('/settings/system/nope', G, 'system', true), { group: 'system', tab: 'overview' });
  assert.deepEqual(settingsRoute('/settings/access/users', G, 'system', true), { group: 'access', tab: null });
});

test('settingsRoute: default landing and unknown groups', () => {
  assert.deepEqual(settingsRoute('/settings', G, 'system', true), { group: 'system', tab: 'overview' });
  assert.deepEqual(settingsRoute('/settings/', G, 'system', true), { group: 'system', tab: 'overview' });
  assert.deepEqual(settingsRoute('/settings/nope', G, 'system', true), { group: 'system', tab: 'overview' });
  assert.deepEqual(settingsRoute('/settings/a/b/c', G, 'system', true), { group: 'system', tab: 'overview' });
  assert.equal(settingsRoute('/dv', G, 'system', true), null);
});

test('settingsRoute: operators and viewers open only About and Account', () => {
  assert.deepEqual(settingsRoute('/settings', OTHER, 'account', false), { group: 'account', tab: null });
  assert.deepEqual(settingsRoute('/settings/about', OTHER, 'account', false), { group: 'about', tab: null });
  assert.equal(settingsRoute('/settings/system', OTHER, 'account', false), null);
  assert.equal(settingsRoute('/settings/system/updates', OTHER, 'account', false), null);
  assert.equal(settingsRoute('/settings/nope', OTHER, 'account', false), null);
  assert.equal(settingsRoute('/settings/a/b/c', OTHER, 'account', false), null);
});

test('clampLines', () => {
  assert.equal(clampLines(5), 50);
  assert.equal(clampLines(99999), 1000);
  assert.equal(clampLines(200.7), 200);
  assert.equal(clampLines(NaN), 200);
});

test('auditSummary', () => {
  assert.equal(auditSummary(''), '');
  assert.equal(auditSummary('{"from":"node2001","to":"node2002"}'), 'from: node2001, to: node2002');
  assert.equal(auditSummary('{}'), '');
  assert.equal(auditSummary('plain'), 'plain');
});

test('the Settings -> Modes pages redirect to the Modes page, the caller lookup to Station', () => {
  assert.equal(movedPath('/settings/modes'), '/modes');
  assert.equal(movedPath('/settings/modes/'), '/modes');
  assert.equal(movedPath('/settings/modes/ysf'), '/modes?mode=ysf');
  assert.equal(movedPath('/settings/modes/echotest'), '/modes?mode=echotest');
  assert.equal(movedPath('/settings/modes/lookup'), '/settings/station/lookup');
  assert.equal(movedPath('/settings/modesx'), null);
});
