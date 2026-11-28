import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isMMDVM, mmdvmConfig, mmdvmError, mmdvmForm, modemBoardText, modemFlagList, modemStateText, prepareText } from './mmdvm.ts';
import type { ModemInfo } from './types.ts';

const flags = { raw: 0, tx: false, rxOverflow: false, txOverflow: false, adcOverflow: false, dacOverflow: false, lockout: false, carrierDetect: false };
const modem = (o: Partial<ModemInfo>): ModemInfo => ({
  configured: true, running: true, connected: true, device: '/dev/serial0', baud: 115200, detectOnly: true,
  duplexCapable: true, duplex: true, flags, rxOverflows: 0, txOverflows: 0, space: [10, 25], reopens: 0, naks: 0, ...o,
});

test('the preset name', () => {
  assert.equal(isMMDVM('mmdvm'), true);
  assert.equal(isMMDVM(' MMDVM '), true);
  assert.equal(isMMDVM('shari'), false);
  assert.equal(isMMDVM(undefined), false);
});

test('defaults round-trip to an empty config', () => {
  const f = mmdvmForm(undefined);
  assert.equal(f.baud, 115200); assert.equal(f.duplex, true); assert.equal(f.allowTx, false);
  assert.equal(mmdvmError(f), '');
  assert.deepEqual(mmdvmConfig(f), {});
  assert.deepEqual(mmdvmConfig(mmdvmForm({ device: '/dev/serial0', baud: 115200, colorCode: 1 })), {});
});

test('settings round-trip, frequencies in MHz', () => {
  const c = { device: '/dev/ttyAMA0', baud: 460800, duplex: false, rxFreqHz: 123456789, txFreqHz: 123456789,
    rxOffsetHz: -250, txOffsetHz: 300, rfPower: 0, colorCode: 0, allowTx: true, resetGpio: 0 };
  const f = mmdvmForm(c);
  assert.equal(f.rxMHz, 123.456789);
  assert.equal(mmdvmError(f), '');
  assert.deepEqual(mmdvmConfig(f), c);
});

test('validation', () => {
  const ok = mmdvmForm(undefined);
  const bad: [Partial<ReturnType<typeof mmdvmForm>>, string][] = [
    [{ device: 'serial0' }, 'serial port'], [{ device: '/dev/../etc/passwd' }, 'serial port'], [{ device: '/dev/..' }, 'serial port'],
    [{ baud: 9600 }, 'baud'], [{ rxMHz: 50 }, 'receive frequency'], [{ txMHz: 2000 }, 'transmit frequency'],
    [{ txOffsetHz: 200000 }, 'transmit offset'], [{ rxOffsetHz: 1.5 }, 'receive offset'], [{ rfPower: 101 }, 'RF power'],
    [{ colorCode: 16 }, 'color code'], [{ resetGpio: -1 }, 'reset GPIO'],
  ];
  for (const [o, want] of bad) assert.match(mmdvmError({ ...ok, ...o }), new RegExp(want), JSON.stringify(o));
  assert.equal(mmdvmError({ ...ok, rxMHz: '', rfPower: '', colorCode: '' }), '');
});

test('status texts', () => {
  assert.equal(modemStateText(null), 'Unknown');
  assert.equal(modemStateText(modem({ configured: false, running: false })), 'Not in use');
  assert.match(modemStateText(modem({ running: false })), /restarts/);
  assert.equal(modemStateText(modem({ connected: false })), 'Not answering on /dev/serial0');
  assert.equal(modemStateText(modem({ state: 'idle' })), 'Connected, idle');
  assert.deepEqual(modemFlagList({ ...flags, rxOverflow: true, lockout: true }), ['RX buffer overflow', 'Lockout']);
  assert.deepEqual(modemFlagList(undefined), []);
  assert.equal(modemBoardText(modem({ board: 'MMDVM_HS' })), 'MMDVM_HS (duplex capable)');
  assert.equal(modemBoardText(modem({ board: 'ZUMspot', duplexCapable: false })), 'ZUMspot (simplex)');
  assert.equal(modemBoardText(modem({})), 'Unrecognized board');
});

test('prepare texts', () => {
  assert.equal(prepareText({ model: '', changes: ['a', 'b'], units: [], rebootNeeded: true }), 'Prepared the Pi (2 changes). Reboot the Pi to finish.');
  assert.equal(prepareText({ model: '', changes: [], units: [], rebootNeeded: false }), 'The Pi was already prepared.');
});
