import { test } from 'node:test';
import assert from 'node:assert/strict';
import { TUNE_HREF, showTuneBanner, wantsAutosetFocus } from './tuning.ts';

const node = (o: { running?: boolean; present?: boolean; needsTuning?: boolean | undefined } = {}) => ({
  running: o.running ?? true,
  radio: { present: o.present ?? true, needsTuning: 'needsTuning' in o ? o.needsTuning : true },
});

test('the reminder shows to an admin when the node needs tuning', () => {
  assert.equal(showTuneBanner('admin', node()), true);
});

test('it never shows to operators, viewers or signed-out visitors', () => {
  for (const r of ['operator', 'viewer', undefined]) assert.equal(showTuneBanner(r, node()), false);
});

test('it is hidden once tuned, without a flag, without a radio, or while the node is down', () => {
  assert.equal(showTuneBanner('admin', node({ needsTuning: false })), false);
  assert.equal(showTuneBanner('admin', node({ needsTuning: undefined })), false);
  assert.equal(showTuneBanner('admin', node({ present: false })), false);
  assert.equal(showTuneBanner('admin', node({ running: false })), false);
  assert.equal(showTuneBanner('admin', null), false);
  assert.equal(showTuneBanner('admin', undefined), false);
});

test('Tune now opens the Audio tab with the focus flag, which the tab reads', () => {
  const [path, query] = TUNE_HREF.replace(/^#/, '').split('?');
  assert.equal(path, '/settings/station/audio');
  assert.equal(wantsAutosetFocus(query!), true);
  assert.equal(wantsAutosetFocus(''), false);
  assert.equal(wantsAutosetFocus('x=1&tune'), true);
  assert.equal(wantsAutosetFocus('other=1'), false);
});
