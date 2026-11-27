// The AMBE vocoder (md380-emu) install status, shared by the Modes page and the Dashboard: one load, one poll while an install runs.
import { api } from './api';
import { refreshModes } from './store.svelte';
import { toast } from './toast.svelte';
import { vocoderBusy, vocoderLastLine } from './modes';
import type { Vocoder } from './types';

export const vocoder = $state<{ status: Vocoder | null; firstSeen: number; dismissed: boolean }>({ status: null, firstSeen: 0, dismissed: false });

/** Take a new status; notes the first time a running job was seen and toasts when a job we watched ends. */
export function setVocoder(v: Vocoder) {
  const was = vocoder.status?.job;
  if (v.job === 'running') { if (was !== 'running') { vocoder.firstSeen = Date.now(); vocoder.dismissed = false; } }
  else if (was === 'running') {
    if (v.job === 'done') { toast('AMBE vocoder installed'); void refreshModes().catch(() => {}); }
    else if (v.job === 'failed') { const l = vocoderLastLine(v.jobLog); toast(l ? `Vocoder install failed: ${l}` : 'Vocoder install failed', 'error'); }
  }
  vocoder.status = v;
}

export async function loadVocoder() { try { setVocoder(await api.vocoder()); } catch { /* the notice stays hidden when the status is unknown */ } }

/** Start the install (admin); the status poll picks it up from there. */
export async function installVocoder() {
  const v = await api.installVocoder();
  setVocoder(v);
  if (v.job === 'running') toast('Installing the AMBE vocoder…');
}

/** Call from an $effect in an admin page: loads the status and polls while an install runs. Returns the cleanup. */
export function trackVocoder(): () => void {
  void loadVocoder();
  const t = setInterval(() => { if (vocoderBusy(vocoder.status)) void loadVocoder(); }, 2000);
  return () => clearInterval(t);
}
