import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isMMDVM, mmdvmConfig, mmdvmError, mmdvmForm, mmdvmFreqError, modemBoardText, modemFlagList, modemRXText, modemStateText, prepareText } from './mmdvm.ts';
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
  const c = { device: '/dev/ttyAMA0', baud: 460800, duplex: false, rxFreqHz: 441456789, txFreqHz: 441456789,
    rxOffsetHz: -250, txOffsetHz: 300, rfPower: 0, colorCode: 0, allowTx: true, resetGpio: 0 };
  const f = mmdvmForm(c);
  assert.equal(f.rxMHz, 441.456789);
  assert.equal(mmdvmError(f), '');
  assert.deepEqual(mmdvmConfig(f), c);
});

test('validation', () => {
  const ok = mmdvmForm(undefined);
  const bad: [Partial<ReturnType<typeof mmdvmForm>>, string][] = [
    [{ device: 'serial0' }, 'serial port'], [{ device: '/dev/../etc/passwd' }, 'serial port'], [{ device: '/dev/..' }, 'serial port'],
    [{ baud: 9600 }, 'baud'], [{ rxMHz: 50 }, 'receive frequency'], [{ rxMHz: 441, txMHz: 436 }, 'bans'], [{ rxMHz: 146 }, 'bans'], [{ rxMHz: 160, duplex: false, txMHz: 160 }, 'none of the bands'], [{ rxMHz: 146.5 }, 'only UHF'], [{ txMHz: 2000 }, 'transmit frequency'],
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

test('frequency rules mirror the modem firmware', () => {
  const ok = (rx: number, tx: number, duplex = true) => mmdvmFreqError(rx, tx, duplex);
  assert.equal(ok(441e6, 441e6), '');
  assert.equal(ok(444.9e6, 439.9e6), '');
  assert.equal(ok(430e6, 430e6, false), '');
  assert.equal(ok(145e6, 145e6, false), '');
  assert.match(ok(437e6, 441e6), /bans/);
  assert.match(ok(435e6, 441e6), /bans/);
  assert.match(ok(441e6, 438e6), /bans/);
  assert.equal(ok(438.000001e6, 441e6), '');
  assert.match(ok(145.8e6, 145.8e6, false), /bans/);
  assert.match(ok(146e6, 146e6, false), /bans/);
  assert.match(ok(441e6, 480e6), /none of the bands/);
  assert.match(ok(441e6, 200e6, false), /none of the bands/);
  assert.match(ok(145e6, 145e6), /below 420 MHz/);
  assert.match(ok(441e6, 475e6), /none of the bands/);
  assert.equal(ok(841e6 + 1e6, 842e6, false), '');
});

test('offsets count and transmit defaults to receive', () => {
  const f = { ...mmdvmForm(undefined), rxMHz: 438.0001, rxOffsetHz: -200 };
  assert.match(mmdvmError(f), /bans/);
  assert.equal(mmdvmError({ ...f, rxOffsetHz: '' }), '');
  assert.equal(mmdvmError({ ...mmdvmForm(undefined), rxMHz: '', txMHz: 436 }), '');
});

test('receive texts', () => {
  assert.equal(modemRXText(null), '');
  assert.equal(modemRXText(modem({ running: false })), '');
  assert.equal(modemRXText(modem({ rxState: 'listening' })), 'Listening on slot 2');
  assert.equal(modemRXText(modem({ rxState: 'receiving', rxCall: { slot: 2, src: 1234567, dst: 91, since: '2026-11-28T00:00:00Z' } })), 'Receiving a call on slot 2: 1234567 → 91');
  assert.match(modemRXText(modem({ rxState: 'receiving', rxCall: { slot: 2, src: 1234567, dst: 7654321, private: true, since: '' } })), /private 7654321/);
  assert.match(modemRXText(modem({ rxState: 'detect' })), /no receive frequency/);
});
