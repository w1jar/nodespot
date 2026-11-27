import { test } from 'node:test';
import assert from 'node:assert/strict';
import { pageTitle } from './title.ts';

test('pageTitle uses the callsign and the page', () => {
  assert.equal(pageTitle('N0CALL', 'Settings'), 'N0CALL · Settings');
  assert.equal(pageTitle('n0call', 'Modes'), 'N0CALL · Modes');
  assert.equal(pageTitle('N0CALL', ''), 'N0CALL');
  assert.equal(pageTitle('N0CALL'), 'N0CALL');
});

test('pageTitle falls back to nodespot without a callsign', () => {
  assert.equal(pageTitle('', 'Settings'), 'nodespot · Settings');
  assert.equal(pageTitle(undefined, ''), 'nodespot');
  assert.equal(pageTitle('  ', 'Modes'), 'nodespot · Modes');
  assert.equal(pageTitle(null), 'nodespot');
});
