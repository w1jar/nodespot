import test from 'node:test';
import assert from 'node:assert/strict';
import { draftOf, savePayload, missingField, dirty, targetText, targetBody, favoritesOf, recentTargets, stateText, linkStatsText, linkStatsTitle, sortModes, modeTitle, needsVocoder, vocoderBusy, vocoderStep, vocoderLastLine, elapsedText, vocoderStartMs, stationValue, stationHint, notReadyText, notReadyShort, ECHO_CODECS, echoCodec, echoCodecValues } from './modes.ts';
import type { ModeInfo, Favorite, Heard, NowCall } from './types';

const bm: ModeInfo = {
  name: 'bm', title: 'BrandMeister', configured: true, active: false,
  fields: [
    { key: 'dmrId', label: 'DMR ID', kind: 'text', required: true },
    { key: 'password', label: 'Password', kind: 'password', required: true, secret: true },
    { key: 'host', label: 'Host', kind: 'text', default: 'master.example.net', advanced: true },
  ],
  settings: { values: { dmrId: '1234567' } }, secretsSet: { password: true },
};

test('draft starts with saved values, defaults and empty secrets', () => {
  assert.deepEqual(draftOf(bm), { dmrId: '1234567', password: '', host: 'master.example.net' });
});

test('a blank secret is left out of the payload', () => {
  const d = draftOf(bm);
  assert.deepEqual(savePayload(bm.fields!, d), { dmrId: '1234567', host: 'master.example.net' });
  d.password = 'pw-example';
  assert.equal(savePayload(bm.fields!, d).password, 'pw-example');
});

test('missing required fields, with a saved secret counting as present', () => {
  const d = draftOf(bm);
  assert.equal(missingField(bm, d), null);
  d.dmrId = ' ';
  assert.equal(missingField(bm, d)?.key, 'dmrId');
  const fresh = { ...bm, secretsSet: {} };
  assert.equal(missingField(fresh, { ...draftOf(fresh) })?.key, 'password');
});

test('a field with a station default is optional when the station has the ID', () => {
  const m: ModeInfo = { ...bm, secretsSet: { password: true }, fields: [{ key: 'dmrId', label: 'DMR ID', kind: 'number', required: true, stationDefault: 'dmrId' }, ...bm.fields!.slice(1)] };
  const d = { ...draftOf(m), dmrId: '' };
  assert.equal(missingField(m, d), null); // the node checks when the station is not known
  assert.equal(missingField(m, d, { dmrId: 1234567 }), null);
  assert.equal(missingField(m, d, {})?.key, 'dmrId');
  assert.equal(missingField(m, { ...d, dmrId: '7654321' }, {}), null);
  assert.equal(missingField(m, d, { nxdnId: 4321 })?.key, 'dmrId');
});

test('the callsign falls back to the station, with the EchoLink suffix', () => {
  const el: ModeInfo = { name: 'echolink', title: 'EchoLink', configured: true, active: false, fields: [
    { key: 'callsign', label: 'EchoLink callsign', kind: 'text', required: true, stationDefault: 'callsign', stationSuffix: '-L' },
    { key: 'password', label: 'EchoLink password', kind: 'password', required: true, secret: true }], secretsSet: { password: true } };
  const d = draftOf(el);
  assert.equal(missingField(el, d, { callsign: 'n0call' }), null);
  assert.equal(missingField(el, d, {})?.key, 'callsign');
  assert.equal(stationValue(el.fields![0], { callsign: 'n0call' }), 'N0CALL-L');
  assert.equal(stationHint(el.fields![0], { callsign: 'n0call' }), 'from station: N0CALL-L');
  assert.equal(stationHint(el.fields![0], {}), 'the station has no callsign yet');
  assert.equal(stationValue({ key: 'id', label: 'DMR ID', kind: 'number', stationDefault: 'dmrId' }, { dmrId: 1234567 }), '1234567');
});

test('the not-ready text names what is missing and says login only for passwords', () => {
  const f = bm.fields!;
  const base = { ...bm, configured: false };
  assert.equal(notReadyText({ ...base, configured: true }), '');
  assert.equal(notReadyText({ ...base, fields: [{ ...f[0], stationDefault: 'dmrId' }, f[1]], missing: ['dmrId'] }), "Set your DMR ID in Settings → Station or on this mode's page.");
  assert.equal(notReadyText({ ...base, missing: ['password'] }), 'BrandMeister needs its password.');
  assert.equal(notReadyText({ ...base, missing: ['host'] }), 'BrandMeister needs its host.');
  assert.equal(notReadyText({ ...base, title: 'TGIF', fields: [f[0], { ...f[1], label: 'Hotspot password' }], missing: ['password'] }), 'TGIF needs its hotspot password.');
  assert.doesNotMatch(notReadyText({ ...base, missing: ['dmrId'] }), /login/);
  assert.equal(notReadyShort({ ...base, missing: ['password'] }), 'Needs password');
  assert.equal(notReadyShort({ ...base }), 'Not set up');
});

test('dirty compares with the saved values', () => {
  const d = draftOf(bm);
  assert.equal(dirty(bm, d), false);
  d.password = 'x';
  assert.equal(dirty(bm, d), true);
});

test('target text and body', () => {
  assert.equal(targetText(undefined), '');
  assert.equal(targetText({ id: '91' }), '91');
  assert.equal(targetText({ id: '91', name: 'Worldwide' }), '91 Worldwide');
  assert.equal(targetText({ id: '91', name: '91' }), '91');
  assert.deepEqual(targetBody(' 91 '), { target: '91' });
  assert.deepEqual(targetBody('YSF1', { host: '192.0.2.5', port: 42000, name: 'Test' }), { target: 'YSF1', name: 'Test', host: '192.0.2.5', port: 42000 });
});

test('favorites of a mode: pinned first, then by position', () => {
  const f = (id: number, mode: string, pinned: boolean, position: number): Favorite => ({ id, mode, target: String(id), label: '', group: '', position, pinned, info: null });
  const out = favoritesOf([f(1, 'bm', false, 0), f(2, 'ysf', false, 0), f(3, 'bm', true, 5), f(4, 'bm', false, 1)], 'bm');
  assert.deepEqual(out.map((x) => x.id), [3, 1, 4]);
});

test('recent targets are distinct and newest first, from calls then heard', () => {
  const calls = [{ id: 'a', kind: 'bm', target: '91', start: '', active: true }, { id: 'b', kind: 'ysf', target: 'X', start: '', active: true }] as NowCall[];
  const heard = [{ mode: 'bm', target: '91', start: '' }, { mode: 'bm', target: '3100', start: '' }, { mode: 'bm', start: '' }] as Heard[];
  assert.deepEqual(recentTargets(heard, calls, 'bm'), ['91', '3100']);
  assert.deepEqual(recentTargets(heard, calls, 'bm', 1), ['91']);
});

test('state text', () => {
  assert.equal(stateText('connected', 'TG 91'), 'Connected · TG 91');
  assert.equal(stateText('idle', 'x'), 'Idle');
  assert.equal(stateText('connected', '', 'login refused'), 'login refused');
  assert.equal(stateText(undefined), '');
});

test('mode order and titles', () => {
  assert.deepEqual(sortModes([{ name: 'zzz' }, { name: 'ysf' }, { name: 'allstar' }, { name: 'aaa' }]).map((m) => m.name), ['allstar', 'ysf', 'aaa', 'zzz']);
  assert.equal(modeTitle('echolink'), 'EchoLink');
  assert.equal(modeTitle('abc'), 'ABC');
  assert.equal(modeTitle('bm', [{ ...bm, title: 'BM Custom' }]), 'BM Custom');
});

test('needsVocoder: only an AMBE mode, and only until the vocoder runs', () => {
  const ambe: ModeInfo = { name: 'ysf', title: 'YSF', configured: true, active: false, needsAmbe: true };
  const plain: ModeInfo = { name: 'm17', title: 'M17', configured: true, active: false };
  const off = { installed: false, active: false, needed: ['ysf'], supported: true };
  assert.equal(needsVocoder(ambe, off), true);
  assert.equal(needsVocoder(ambe, { ...off, installed: true }), true);
  assert.equal(needsVocoder(ambe, { ...off, installed: true, active: true }), false);
  assert.equal(needsVocoder(plain, off), false);
  assert.equal(needsVocoder(ambe, null), false);
  assert.equal(needsVocoder(undefined, off), false);
});

test('vocoderBusy: while the install job runs', () => {
  const v = { installed: false, active: false, needed: [], supported: true };
  assert.equal(vocoderBusy({ ...v, job: 'running' }), true);
  assert.equal(vocoderBusy({ ...v, job: 'failed' }), false);
  assert.equal(vocoderBusy(v), false);
  assert.equal(vocoderBusy(null), false);
});

test('linkStatsText is compact and hides zero extras', () => {
  assert.equal(linkStatsText(undefined), '');
  const base = { rxVoiceFull: 3, rxVoiceMini: 120, txVoice: 0, vnakSent: 0, vnakRecv: 0, retransmits: 0, parseErrors: 0, dropped: 0, gaps: 0 };
  assert.equal(linkStatsText(base), 'rx 3+120 · tx 0 · VNAK 0/0 · retx 0');
  assert.equal(linkStatsText({ ...base, gaps: 1, dropped: 30, parseErrors: 2 }), 'rx 3+120 · tx 0 · VNAK 0/0 · retx 0 · gaps 1 · dropped 30 · bad 2');
});

test('linkStatsText shows EchoLink packet counters', () => {
  const base = { proto: 'echolink', rxVoiceFull: 310, rxVoiceMini: 0, txVoice: 25, vnakSent: 0, vnakRecv: 0, retransmits: 0, parseErrors: 0, dropped: 0, gaps: 0 };
  assert.equal(linkStatsText(base), 'rx 310 pkts · tx 25 pkts');
  assert.equal(linkStatsText({ ...base, late: 2, dropped: 4, parseErrors: 1, txErrors: 3 }), 'rx 310 pkts · tx 25 pkts · late 2 · dropped 4 · bad 1 · send errors 3');
  assert.equal(linkStatsText(base, 50), 'rx 310 pkts · tx 25 pkts · not played 50');
  assert.equal(linkStatsText(base, 0), 'rx 310 pkts · tx 25 pkts');
  assert.match(linkStatsTitle(base), /audio packets/);
  assert.match(linkStatsTitle({ ...base, proto: undefined }), /VNAK/);
});

// ---- The Modes page helpers.
import { visibleModes, hiddenModes, pickerModes, toggleHidden } from './modes.ts';
import { CONFIRM_MS, pendingOutcome, pickerKind, m17Tune, m17Split, targetValue, modeChip, favoritesInOrder, movedIds, findFavorite, favoriteBlock, defaultFavName, validFavName, favoriteActive, pinnedFavorites, hasTarget, M17_MODULES } from './modes.ts';

const fav = (id: number, mode: string, target: string, over: Partial<Favorite> = {}): Favorite => ({ id, mode, target, label: target, group: '', position: id, pinned: false, info: null, ...over });

test('a mode switch is confirmed, waiting or not confirmed after five seconds', () => {
  assert.equal(pendingOutcome(null, 'bm', 1000), 'none');
  const p = { mode: 'ysf', since: 1000 };
  assert.equal(pendingOutcome(p, 'ysf', 1100), 'confirmed');
  assert.equal(pendingOutcome(p, 'bm', 1000 + CONFIRM_MS - 1), 'waiting');
  assert.equal(pendingOutcome(p, 'bm', 1000 + CONFIRM_MS), 'unconfirmed');
  assert.equal(pendingOutcome({ mode: '', since: 0 }, undefined, 10), 'confirmed');
});

test('each mode gets the right kind of target picker', () => {
  const m = (name: string, needsHost = false) => ({ name, title: name, configured: true, active: false, needsHost }) as ModeInfo;
  assert.equal(pickerKind(m('allstar')), 'nodes');
  assert.equal(pickerKind(m('echolink')), 'nodes');
  assert.equal(pickerKind(m('ysf', true)), 'hosts');
  assert.equal(pickerKind(m('m17', true)), 'hosts');
  assert.equal(pickerKind(m('bm')), 'plain');
  assert.equal(pickerKind(m('echotest')), 'none');
  assert.equal(pickerKind(undefined), 'none');
  assert.equal(hasTarget(m('echotest')), false);
});

test('the M17 target joins the reflector and the module', () => {
  assert.equal(M17_MODULES.length, 26);
  assert.equal(m17Tune('m17-xxx', 'c'), 'M17-XXX C');
  assert.equal(m17Tune(' M17-XXX  b ', 'C'), 'M17-XXX B');
  assert.equal(m17Tune('nonsense', 'C'), '');
  assert.deepEqual(m17Split('M17-XXX C'), { reflector: 'M17-XXX', module: 'C' });
  assert.equal(m17Split('M17-XXX'), null);
  assert.equal(targetValue('m17', 'M17-XXX', 'D'), 'M17-XXX D');
  assert.equal(targetValue('bm', ' 91 ', 'D'), '91');
});

test('a mode button shows not set up, ready, or the link state of the active mode', () => {
  const base = { name: 'bm', title: 'BM', configured: true, active: false } as ModeInfo;
  assert.deepEqual(modeChip({ ...base, configured: false }, 'bm', 'connected'), { tone: 'unset', text: 'Set up' });
  assert.deepEqual(modeChip(base, '', undefined), { tone: 'off', text: 'Ready' });
  assert.deepEqual(modeChip(base, 'bm', 'connected'), { tone: 'ok', text: 'Connected' });
  assert.deepEqual(modeChip(base, 'bm', 'connecting'), { tone: 'warn', text: 'Connecting' });
  assert.deepEqual(modeChip(base, 'bm', 'error'), { tone: 'err', text: 'Error' });
  assert.deepEqual(modeChip({ ...base, name: 'echotest' }, 'echotest', 'connected'), { tone: 'ok', text: 'Running' });
  assert.deepEqual(modeChip({ ...base, error: 'x' }, 'allstar', 'connected'), { tone: 'err', text: 'Error' });
});

test('favorites keep the server order and move one place at a time', () => {
  const all = [fav(1, 'bm', '91'), fav(2, 'ysf', 'YSF-TEST'), fav(3, 'bm', '3100'), fav(4, 'bm', '9', { pinned: true })];
  const bm = favoritesInOrder(all, 'bm');
  assert.deepEqual(bm.map((f) => f.id), [1, 3, 4]);
  assert.deepEqual(movedIds(bm, 3, -1), [3, 1, 4]);
  assert.deepEqual(movedIds(bm, 3, 1), [1, 4, 3]);
  assert.equal(movedIds(bm, 1, -1), null);
  assert.equal(movedIds(bm, 4, 1), null);
  assert.equal(movedIds(bm, 99, 1), null);
  assert.deepEqual(pinnedFavorites(all).map((f) => f.id), [4]);
});

test('saving the current target as a favorite is blocked when empty or already saved', () => {
  const all = [fav(1, 'bm', '91')];
  assert.equal(favoriteBlock(all, 'bm', ' '), 'Enter or connect a target first');
  assert.equal(favoriteBlock(all, 'bm', '91'), 'Already a favorite');
  assert.equal(favoriteBlock(all, 'ysf', '91'), '');
  assert.equal(findFavorite(all, 'bm', ' 91 ')?.id, 1);
});

test('favorite names: default and validation', () => {
  assert.equal(defaultFavName('91', 'World-wide'), 'World-wide');
  assert.equal(defaultFavName('91', '91'), '91');
  assert.equal(defaultFavName('91'), '91');
  assert.equal(defaultFavName('x', 'y'.repeat(60)).length, 40);
  assert.equal(validFavName('  Club  '), 'Club');
  assert.equal(validFavName('   '), null);
  assert.equal(validFavName('a'.repeat(41)), null);
  assert.equal(validFavName('bad\u0007name'), null);
});

test('a favorite is active when its mode is active and the target matches', () => {
  assert.equal(favoriteActive({ mode: 'bm', target: '91' }, 'bm', { id: '91' }), true);
  assert.equal(favoriteActive({ mode: 'bm', target: '91' }, 'ysf', { id: '91' }), false);
  assert.equal(favoriteActive({ mode: 'bm', target: '91' }, 'bm', { id: '3100' }), false);
  assert.equal(favoriteActive({ mode: 'bm', target: '91' }, 'bm', undefined), false);
  assert.equal(favoriteActive({ mode: 'm17', target: 'M17-XXX C' }, 'm17', { id: 'M17-XXX' }), true);
  assert.equal(favoriteActive({ mode: 'ysf', target: 'ysf-test' }, 'ysf', { id: 'YSF-TEST' }), true);
});

test('echo test codec: choices, saved value and the values to save', () => {
  assert.deepEqual(ECHO_CODECS.map((o) => o.value), ['plain', 'ambe', 'imbe', 'codec2', 'gsm', 'ulaw']);
  assert.equal(ECHO_CODECS.find((o) => o.value === 'ulaw')?.label, 'µ-law (AllStar)');
  const m = { name: 'echotest', title: 'Echo test', configured: true, active: false, settings: { values: { codec: 'AMBE', playback_delay_ms: '1500' } } } as ModeInfo;
  assert.equal(echoCodec(m), 'ambe');
  assert.equal(echoCodec(undefined), 'plain');
  assert.equal(echoCodec({ ...m, settings: { values: { codec: 'bogus' } } }), 'plain');
  assert.deepEqual(echoCodecValues(m, 'gsm'), { codec: 'gsm', playback_delay_ms: '1500' });
  assert.deepEqual(echoCodecValues(undefined, 'imbe'), { codec: 'imbe' });
});

test('hidden modes leave the row unless active; pickers keep the mode a favorite or schedule uses', () => {
  const l = [{ name: 'allstar', configured: true }, { name: 'bm', configured: true }, { name: 'nxdn', configured: true }, { name: 'm17', configured: false }];
  const names = (x: { name: string }[]) => x.map((m) => m.name);
  assert.deepEqual(names(visibleModes(l, undefined, '')), ['allstar', 'bm', 'nxdn', 'm17'], 'a fresh install shows all');
  assert.deepEqual(names(visibleModes(l, ['bm', 'nxdn'], '')), ['allstar', 'm17']);
  assert.deepEqual(names(visibleModes(l, ['bm', 'nxdn'], 'bm')), ['allstar', 'bm', 'm17'], 'the active mode stays visible');
  assert.deepEqual(names(hiddenModes(l, ['bm', 'nxdn'], 'bm')), ['nxdn']);
  assert.deepEqual(names(hiddenModes(l, [], '')), []);
  assert.deepEqual(names(pickerModes(l, ['bm'])), ['allstar', 'nxdn'], 'hidden and not-ready modes are not offered');
  assert.deepEqual(names(pickerModes(l, ['bm'], ['bm'])), ['allstar', 'bm', 'nxdn'], 'an existing favorite keeps its mode');
  assert.deepEqual(toggleHidden(['nxdn'], 'bm', true), ['bm', 'nxdn']);
  assert.deepEqual(toggleHidden(['bm', 'nxdn'], 'bm', true), ['bm', 'nxdn']);
  assert.deepEqual(toggleHidden(['bm', 'nxdn'], 'bm', false), ['nxdn']);
  assert.deepEqual(toggleHidden(undefined, 'bm', false), []);
});

test('EchoLink results read as location, kind and status', async () => {
  const { echoDetail, echoKindLabel, echoStatusLabel } = await import('./modes.ts');
  assert.equal(echoDetail({ location: 'Springfield, ST', kind: 'link', status: 'online' }), 'Springfield, ST · Link · Online');
  assert.equal(echoDetail({ kind: 'conference', status: 'busy' }), 'Conference · Busy');
  assert.equal(echoDetail({ kind: 'repeater' }), 'Repeater');
  assert.equal(echoKindLabel('nope'), 'User');
  assert.equal(echoStatusLabel(''), '');
  assert.equal(echoStatusLabel('away'), 'Away');
});

test('vocoderStep: the latest meaningful apt line decides', () => {
  assert.equal(vocoderStep(''), 'starting');
  assert.equal(vocoderStep(undefined), 'starting');
  assert.equal(vocoderStep('Something odd'), 'starting');
  assert.equal(vocoderStep('Waiting for cache lock: Could not get lock /var/lib/dpkg/lock-frontend'), 'waiting for other system updates to finish');
  assert.equal(vocoderStep('E: Could not get lock /var/lib/dpkg/lock'), 'waiting for other system updates to finish');
  assert.equal(vocoderStep('Waiting for dpkg frontend lock'), 'waiting for other system updates to finish');
  assert.equal(vocoderStep('Get:1 http://192.0.2.1/debian stable InRelease'), 'downloading');
  assert.equal(vocoderStep('Hit:2 http://192.0.2.1/debian stable'), 'downloading');
  assert.equal(vocoderStep('Fetched 12 MB in 3s\nReading package lists...'), 'downloading');
  assert.equal(vocoderStep('Get:1 x\nUnpacking foo (1.0) ...\n'), 'installing');
  assert.equal(vocoderStep('Selecting previously unselected package foo.'), 'installing');
  assert.equal(vocoderStep('Setting up foo (1.0) ...'), 'installing');
  assert.equal(vocoderStep('Waiting for cache lock\nGet:1 x\nsome noise\n'), 'downloading');
});

test('vocoderLastLine: the last non-empty line', () => {
  assert.equal(vocoderLastLine('a\nE: Unable to locate package x\n\n'), 'E: Unable to locate package x');
  assert.equal(vocoderLastLine(''), '');
  assert.equal(vocoderLastLine(undefined), '');
});

test('elapsedText: seconds, then minutes and seconds', () => {
  assert.equal(elapsedText(1000, 1000), '0 s');
  assert.equal(elapsedText(0, 45_000), '45 s');
  assert.equal(elapsedText(0, 60_000), '1 min 0 s');
  assert.equal(elapsedText(0, 130_500), '2 min 10 s');
  assert.equal(elapsedText(5000, 1000), '0 s');
});

test('vocoderStartMs: jobStarted wins, else when the page first saw the job', () => {
  const v = { installed: false, active: false, needed: [], supported: true, job: 'running' as const };
  assert.equal(vocoderStartMs({ ...v, jobStarted: '2026-01-02T03:04:05Z' }, 99), Date.parse('2026-01-02T03:04:05Z'));
  assert.equal(vocoderStartMs(v, 99), 99);
  assert.equal(vocoderStartMs({ ...v, jobStarted: 'garbage' }, 99), 99);
  assert.equal(vocoderStartMs(null, 99), 99);
});
