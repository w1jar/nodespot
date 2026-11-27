// Run with `npm test`. Keyed {#each} blocks that get a duplicate key abort rendering, so every keyed each in the call lists must include the loop index.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';

const root = new URL('../components/', import.meta.url);
function files(dir: URL, rel = ''): string[] {
  const out: string[] = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.isDirectory()) out.push(...files(new URL(e.name + '/', dir), rel + e.name + '/'));
    else if (e.name.endsWith('.svelte')) out.push(rel + e.name);
  }
  return out;
}

for (const f of ['Dashboard', 'Modes', 'NowCalling', 'Schedules']) {
  test(f + ' exists', () => assert.ok(files(root).includes(f + '.svelte')));
}

for (const f of ['LastHeardTable', 'NowCalling', 'NowCallingSheet'].map((x) => x + '.svelte')) {
  test(f + ' each keys include the index', () => {
    const src = readFileSync(new URL(f, root), 'utf8');
    const each = src.match(/\{#each [^}]*\([^}]*\)\}/g) ?? [];
    for (const e of each) assert.match(e, /, i \(.*\bi\b/, e);
  });
}
