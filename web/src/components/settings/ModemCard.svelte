<script lang="ts">
  // The MMDVM modem (the mmdvm preset): what the node found on the serial port, the Pi's serial setup, and the admin actions
  // "Prepare the Pi" (frees the UART for the hat) and "Reset modem" (closes the port and opens it again). Early preview: the
  // node sets up the hat in simplex and passes DMR calls heard from a handheld on slot 2 to the linked DMR network. It never transmits on RF yet.
  import { api } from '../../lib/api';
  import { isAdmin } from '../../lib/store.svelte';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { modemBoardText, modemFlagList, modemRXText, modemStateText, prepareText } from '../../lib/mmdvm';
  import type { ModemInfo, RFDeviceCheck, RFPrepareResult } from '../../lib/types';

  const admin = $derived(isAdmin());
  let m = $state<ModemInfo | null>(null), chk = $state<RFDeviceCheck | null>(null), prep = $state<RFPrepareResult | null>(null);
  let noHelper = $state(false), busy = $state('');

  async function loadModem() { try { m = await api.modem(); } catch { m = null; } }
  async function loadCheck() {
    try { chk = await api.radioDeviceCheck(); noHelper = false; } catch { chk = null; noHelper = true; }
  }
  $effect(() => {
    loadModem(); loadCheck();
    const t = setInterval(loadModem, 3000);
    return () => clearInterval(t);
  });

  async function prepare() {
    busy = 'prepare';
    try { prep = await api.radioPrepare(); toast(prepareText(prep), prep.rebootNeeded ? 'info' : 'ok', 7000); await loadCheck(); }
    catch (e) { toastError(e); } finally { busy = ''; }
  }
  async function reset() {
    busy = 'reset';
    try { m = await api.modemReset(); toast('Modem reopened'); } catch (e) { toastError(e); } finally { busy = ''; }
  }
  const rx = $derived(modemRXText(m));
  const flags = $derived(modemFlagList(m?.flags));
  const reboot = $derived(!!prep?.rebootNeeded || !!chk?.rebootNeeded);
  const ok = $derived(!!m?.connected);
</script>

<section class="card mt-4 space-y-3 p-4" aria-label="MMDVM modem" data-testid="modem-card">
  <div class="flex flex-wrap items-center gap-2">
    <h2 class="text-base font-semibold">MMDVM modem</h2>
    <span class="badge" class:text-ok={ok} class:text-danger={!!m?.running && !ok} data-testid="modem-state">{modemStateText(m)}</span>
  </div>
  <p class="text-xs text-muted">Early preview: the node sets up the hat in simplex and passes DMR calls heard from a handheld on slot 2 to the linked DMR network. It never transmits on RF yet.</p>

  {#if m?.running}
    <dl class="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[auto_1fr]">
      <dt class="text-muted">Serial port</dt><dd class="font-mono">{m.device} at {m.baud}</dd>
      {#if m.connected || m.board}
        <dt class="text-muted">Board</dt><dd data-testid="modem-board">{modemBoardText(m)}</dd>
        <dt class="text-muted">Firmware</dt><dd>{m.firmware || 'Unknown'}{m.buildDate ? `, built ${m.buildDate}` : ''}</dd>
        <dt class="text-muted">Protocol</dt><dd>{m.protocol ?? 'Unknown'}</dd>
        {#if m.modes?.length}<dt class="text-muted">Modes</dt><dd>{m.modes.join(', ')}</dd>{/if}
        {#if rx}<dt class="text-muted">Receive</dt><dd data-testid="modem-rx" class:text-ok={m.rxState === 'receiving'}>{rx}</dd>{/if}
        <dt class="text-muted">State</dt><dd>{m.state || 'Unknown'}</dd>
        <dt class="text-muted">Flags</dt><dd>{flags.length ? flags.join(', ') : 'None'}</dd>
        <dt class="text-muted">Overflows</dt><dd class="tabular-nums">RX {m.rxOverflows}, TX {m.txOverflows}</dd>
      {/if}
      {#if m.reopens}<dt class="text-muted">Reopened</dt><dd class="tabular-nums">{m.reopens} time{m.reopens === 1 ? '' : 's'}</dd>{/if}
      {#if m.lastError}<dt class="text-muted">Last error</dt><dd class="text-danger" data-testid="modem-error">{m.lastError}</dd>{/if}
    </dl>
    {#if m.notes?.length}<ul class="list-disc pl-5 text-xs text-muted" data-testid="modem-notes">{#each m.notes as n}<li>{n}</li>{/each}</ul>{/if}
  {:else if m?.configured}
    <p class="text-sm text-muted">Save the settings below; the node restarts to open the modem.</p>
  {/if}

  {#if chk}
    <div class="rounded-md border border-line p-3 text-sm" data-testid="modem-check">
      <p class="font-medium">Pi serial setup{chk.model ? ` (${chk.model})` : ''}</p>
      <ul class="mt-1 space-y-0.5 text-xs text-muted">
        <li>{chk.device}: {chk.exists ? `present${chk.linkTarget ? `, points to ${chk.linkTarget}` : ''}` : 'missing'}{chk.exists ? (chk.nodeAccess ? ', the node can open it' : `, the node cannot open it (${chk.group}, ${chk.mode})`) : ''}</li>
        <li>UART {chk.uartEnabled ? 'enabled' : 'not enabled'}, Bluetooth {chk.btDisabled ? 'moved off the UART' : 'still on the UART'}, serial console {chk.consoleOnUart || chk.consoleRunning ? 'still on the UART' : 'off'}</li>
      </ul>
      {#if chk.problems?.length}<ul class="mt-1 list-disc pl-5 text-xs text-danger">{#each chk.problems as p}<li>{p}</li>{/each}</ul>{/if}
    </div>
  {:else if noHelper}
    <p class="text-xs text-muted">The Pi's serial setup cannot be checked here (not installed from a package).</p>
  {/if}

  {#if prep?.changes.length}
    <ul class="list-disc pl-5 text-xs text-muted" data-testid="modem-prepared">{#each prep.changes as c}<li>{c}</li>{/each}</ul>
  {/if}
  {#if reboot}<p class="text-sm text-warn" role="status" data-testid="modem-reboot">Reboot the Pi to apply the serial settings.</p>{/if}

  {#if admin}
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn btn-sm btn-accent-outline" disabled={!!busy || noHelper} onclick={prepare} data-testid="modem-prepare">{busy === 'prepare' ? 'Preparing…' : 'Prepare the Pi'}</button>
      <button type="button" class="btn btn-sm" disabled={!!busy || !m?.running} onclick={reset} data-testid="modem-reset">{busy === 'reset' ? 'Resetting…' : 'Reset modem'}</button>
    </div>
    <p class="text-xs text-muted">Prepare the Pi turns the serial console off on the UART, moves Bluetooth off it and stops the services that hold it; it changes nothing when the Pi is already prepared. Reset modem closes the serial port and opens it again.</p>
  {/if}
</section>
