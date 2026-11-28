// The MMDVM modem (the mmdvm radio preset): the settings form and the status card's texts. Pure functions, tested in mmdvm.test.ts.
import type { MMDVMConfig, ModemFlags, ModemInfo, RFPrepareResult } from './types';

export const MMDVM_PRESET = 'mmdvm';
export const MMDVM_DEFAULT_DEVICE = '/dev/serial0';
export const MMDVM_DEFAULT_BAUD = 115200;
export const MMDVM_BAUDS = [38400, 57600, 115200, 230400, 460800];
export const MMDVM_DEFAULT_COLOR_CODE = 1;

/** True when the radio preset is the MMDVM modem. */
export const isMMDVM = (preset: string | undefined): boolean => (preset ?? '').trim().toLowerCase() === MMDVM_PRESET;

/** A number field as the form holds it: empty means unset. */
type Num = number | '' | null | undefined;

/** The settings form: frequencies in MHz (the node keeps Hz). */
export interface MMDVMForm {
  device: string; baud: number; duplex: boolean;
  rxMHz: Num; txMHz: Num; rxOffsetHz: Num; txOffsetHz: Num;
  rfPower: Num; colorCode: Num; allowTx: boolean; resetGpio: Num;
}

const blank = (v: Num): v is '' | null | undefined => v === '' || v === null || v === undefined || Number.isNaN(v);

/** The form for saved settings (unset values show the defaults or stay empty). */
export function mmdvmForm(c: MMDVMConfig | undefined): MMDVMForm {
  const mhz = (hz?: number) => (hz ? +(hz / 1e6).toFixed(6) : '');
  return {
    device: c?.device ?? '', baud: c?.baud || MMDVM_DEFAULT_BAUD, duplex: c?.duplex ?? true,
    rxMHz: mhz(c?.rxFreqHz), txMHz: mhz(c?.txFreqHz), rxOffsetHz: c?.rxOffsetHz ?? '', txOffsetHz: c?.txOffsetHz ?? '',
    rfPower: c?.rfPower ?? '', colorCode: c?.colorCode ?? '', allowTx: !!c?.allowTx, resetGpio: c?.resetGpio ?? '',
  };
}

const inBand = (f: number) => (f >= 144e6 && f < 148e6) || (f >= 219e6 && f < 225e6) || (f >= 420e6 && f < 475e6) || (f >= 842e6 && f < 950e6);
const banned = (f: number) => (f >= 145.8e6 && f <= 146e6) || (f >= 435e6 && f <= 438e6);

/** MHz as the node's messages print it: up to six decimals, no trailing zeros. */
const mhz = (hz: number) => `${+(hz / 1e6).toFixed(6)} MHz`;

/**
 * Why the modem would refuse these frequencies (Hz, offsets already applied), or ''. Mirrors CheckFreq in
 * internal/native/radio/mmdvm/freqcheck.go: the MMDVM_HS firmware's SET_FREQ limits. Bands are 144 to 148, 219 to 225, 420 to 475
 * and 842 to 950 MHz; 145.8 to 146.0 and 435 to 438 MHz are banned; a dual-chip duplex hat (duplex on, the default) takes only UHF.
 */
export function mmdvmFreqError(rxHz: number, txHz: number, duplex: boolean): string {
  for (const [name, f] of [['receive', rxHz], ['transmit', txHz]] as const) {
    if (banned(f)) return `The ${name} frequency ${mhz(f)} is in a range the modem bans (145.8 to 146.0 and 435 to 438 MHz)`;
    if (duplex && f < 420e6) return `The ${name} frequency ${mhz(f)} is below 420 MHz, and a dual-chip duplex hat takes only UHF`;
  }
  if (!inBand(txHz)) return `The transmit frequency ${mhz(txHz)} is in none of the bands the modem takes (144 to 148, 219 to 225, 420 to 475, 842 to 950 MHz)`;
  return '';
}

/** The frequencies the node sends the modem (Hz, with the trim offsets; transmit defaults to receive), or null with no receive frequency. */
export function mmdvmModemFreq(f: MMDVMForm): { rx: number; tx: number } | null {
  if (blank(f.rxMHz) || !+f.rxMHz) return null;
  const rx = Math.round(+f.rxMHz * 1e6);
  const tx = !blank(f.txMHz) && +f.txMHz ? Math.round(+f.txMHz * 1e6) : rx;
  return { rx: rx + (blank(f.rxOffsetHz) ? 0 : +f.rxOffsetHz), tx: tx + (blank(f.txOffsetHz) ? 0 : +f.txOffsetHz) };
}

/** An error text for the form, or ''. The node checks the same rules. */
export function mmdvmError(f: MMDVMForm): string {
  const dev = f.device.trim();
  if (dev && (!/^\/dev\/[A-Za-z0-9._-]+$/.test(dev) || dev === '/dev/.' || dev === '/dev/..')) return `The serial port must be a device in /dev, such as ${MMDVM_DEFAULT_DEVICE}`;
  if (!MMDVM_BAUDS.includes(+f.baud)) return `The baud rate must be one of ${MMDVM_BAUDS.join(', ')}`;
  for (const [name, v] of [['receive', f.rxMHz], ['transmit', f.txMHz]] as const) {
    if (!blank(v) && (+v < 100 || +v > 1000)) return `The ${name} frequency must be 100 to 1000 MHz`;
  }
  for (const [name, v] of [['receive', f.rxOffsetHz], ['transmit', f.txOffsetHz]] as const) {
    if (!blank(v) && (!Number.isInteger(+v) || Math.abs(+v) > 100000)) return `The ${name} offset must be a whole number from -100000 to 100000 Hz`;
  }
  if (!blank(f.rfPower) && (!Number.isInteger(+f.rfPower) || +f.rfPower < 0 || +f.rfPower > 100)) return 'RF power must be 0 to 100%';
  if (!blank(f.colorCode) && (!Number.isInteger(+f.colorCode) || +f.colorCode < 0 || +f.colorCode > 15)) return 'The color code must be 0 to 15';
  if (!blank(f.resetGpio) && (!Number.isInteger(+f.resetGpio) || +f.resetGpio < 0 || +f.resetGpio > 1023)) return 'The reset GPIO must be 0 to 1023';
  const hz = mmdvmModemFreq(f);
  return hz ? mmdvmFreqError(hz.rx, hz.tx, f.duplex) : '';
}

/** The settings to save: empty fields and defaults are left out. */
export function mmdvmConfig(f: MMDVMForm): MMDVMConfig {
  const c: MMDVMConfig = {};
  const dev = f.device.trim();
  if (dev && dev !== MMDVM_DEFAULT_DEVICE) c.device = dev;
  if (+f.baud && +f.baud !== MMDVM_DEFAULT_BAUD) c.baud = +f.baud;
  if (!f.duplex) c.duplex = false;
  if (!blank(f.rxMHz) && +f.rxMHz) c.rxFreqHz = Math.round(+f.rxMHz * 1e6);
  if (!blank(f.txMHz) && +f.txMHz) c.txFreqHz = Math.round(+f.txMHz * 1e6);
  if (!blank(f.rxOffsetHz) && +f.rxOffsetHz) c.rxOffsetHz = +f.rxOffsetHz;
  if (!blank(f.txOffsetHz) && +f.txOffsetHz) c.txOffsetHz = +f.txOffsetHz;
  if (!blank(f.rfPower)) c.rfPower = +f.rfPower;
  if (!blank(f.colorCode) && +f.colorCode !== MMDVM_DEFAULT_COLOR_CODE) c.colorCode = +f.colorCode;
  if (f.allowTx) c.allowTx = true;
  if (!blank(f.resetGpio)) c.resetGpio = +f.resetGpio;
  return c;
}

/** The receive line of the modem card, from the node's rxState and rxCall; '' when it has nothing to say. */
export function modemRXText(m: ModemInfo | null | undefined): string {
  if (!m?.running) return '';
  switch (m.rxState) {
    case 'receiving': {
      const c = m.rxCall;
      return c ? `Receiving a call on slot ${c.slot}: ${c.src} → ${c.private ? 'private ' : ''}${c.dst}` : 'Receiving a call';
    }
    case 'listening': return 'Listening on slot 2';
    case 'configured': return 'Configured, starting to listen';
    case 'detect': return 'Not receiving: no receive frequency is set';
    default: return '';
  }
}

/** The status line of the modem card. */
export function modemStateText(m: ModemInfo | null | undefined): string {
  if (!m) return 'Unknown';
  if (!m.configured) return 'Not in use';
  if (!m.running) return 'Starting (the node restarts to use the modem)';
  if (!m.connected) return `Not answering on ${m.device}`;
  return m.state ? `Connected, ${m.state}` : 'Connected';
}

const FLAG_NAMES: [keyof ModemFlags, string][] = [
  ['tx', 'Transmitting'], ['rxOverflow', 'RX buffer overflow'], ['txOverflow', 'TX buffer overflow'],
  ['adcOverflow', 'ADC overflow'], ['dacOverflow', 'DAC overflow'], ['lockout', 'Lockout'], ['carrierDetect', 'Carrier detect'],
];

/** The flags the modem last reported, in words. */
export function modemFlagList(f: ModemFlags | undefined): string[] {
  if (!f) return [];
  return FLAG_NAMES.filter(([k]) => f[k]).map(([, n]) => n);
}

/** "MMDVM_HS, dual (duplex capable)" and the like. */
export function modemBoardText(m: ModemInfo): string {
  if (!m.board) return m.connected ? 'Unrecognized board' : '';
  return m.board + (m.duplexCapable ? ' (duplex capable)' : ' (simplex)');
}

/** What Prepare the Pi did, for a toast. */
export function prepareText(r: RFPrepareResult): string {
  const did = r.changes.length ? `Prepared the Pi (${r.changes.length} change${r.changes.length === 1 ? '' : 's'}).` : 'The Pi was already prepared.';
  return r.rebootNeeded ? `${did} Reboot the Pi to finish.` : did;
}
