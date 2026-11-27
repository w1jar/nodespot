<script lang="ts">
  import { api } from '../../lib/api';
  import type { SysLogs, SysServices } from '../../lib/types';
  import Refresh from '@lucide/svelte/icons/refresh-cw';

  let units = $state<string[]>([]), unit = $state('nodespot-node'), lines = $state(200), since = $state('');
  let logs = $state<SysLogs | null>(null), logErr = $state(''), logBusy = $state(false);
  let svc = $state<SysServices | null>(null), svcErr = $state('');

  async function loadLogs() {
    logBusy = true;
    try { logs = await api.sysLogs(unit, lines, since); logErr = ''; } catch (e) { logErr = e instanceof Error ? e.message : String(e); } finally { logBusy = false; }
  }
  async function loadSvc() {
    try { svc = await api.sysServices(); svcErr = ''; } catch (e) { svcErr = e instanceof Error ? e.message : String(e); }
  }
  $effect(() => {
    api.sysLogUnits().then((r) => { units = r.units; if (r.units.length && !r.units.includes(unit)) unit = r.units[0]; }).catch(() => {});
    loadSvc();
  });
  $effect(() => { unit; lines; since; loadLogs(); });
  const tone = (a: string) => (a === 'active' ? 'text-ok' : a === 'failed' ? 'text-danger' : '');
</script>

<section class="card overflow-x-auto" aria-label="Services">
  <div class="flex items-center justify-between px-3 py-2">
    <h3 class="text-base font-semibold">Services</h3>
    <button class="btn btn-sm" onclick={loadSvc}><Refresh size={13} />Refresh</button>
  </div>
  {#if svcErr}
    <p class="px-3 pb-3 text-sm text-danger" role="alert">{svcErr}</p>
  {:else if !svc}
    <p class="px-3 pb-3 text-sm text-muted" role="status">Loading…</p>
  {:else if !svc.available}
    <p class="px-3 pb-3 text-sm text-muted">Not available: systemctl is missing on this system.</p>
  {:else}
    <table class="tbl">
      <thead><tr><th>Service</th><th>State</th><th>Enabled</th><th>Since</th></tr></thead>
      <tbody>
        {#each svc.services as s (s.unit)}
          <tr>
            <td><div class="font-medium">{s.unit}</div>{#if s.description}<div class="text-xs text-muted">{s.description}</div>{/if}</td>
            <td><span class="badge {tone(s.active)}">{s.active}{#if s.sub && s.sub !== s.active} / {s.sub}{/if}</span></td>
            <td class="text-xs">{s.enabled}</td>
            <td class="text-xs text-muted">{s.since}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</section>

<section class="card space-y-3 p-4" aria-label="Logs">
  <div class="flex flex-wrap items-end gap-3">
    <h3 class="mr-auto text-base font-semibold">Logs</h3>
    <label><span class="label">Unit</span>
      <select class="input" bind:value={unit}>{#each units as u (u)}<option value={u}>{u}</option>{:else}<option value={unit}>{unit}</option>{/each}</select></label>
    <label><span class="label">Lines</span>
      <select class="input" bind:value={lines}>{#each [100, 200, 500, 1000] as n}<option value={n}>{n}</option>{/each}</select></label>
    <label><span class="label">Since</span>
      <select class="input" bind:value={since}><option value="">Any time</option><option value="1h">Last hour</option><option value="24h">Last 24 hours</option><option value="boot">This boot</option></select></label>
    <button class="btn btn-sm" disabled={logBusy} onclick={loadLogs}><Refresh size={13} />Refresh</button>
  </div>
  {#if logErr}
    <p class="text-sm text-danger" role="alert">{logErr}</p>
  {:else if logs && !logs.available}
    <p class="text-sm text-muted">Not available: journalctl is missing on this system.</p>
  {:else if logs}
    <pre class="max-h-[60vh] overflow-auto rounded border border-line bg-surface-2 p-2 font-mono text-xs leading-snug whitespace-pre-wrap break-all" aria-label="Log text">{logs.text || 'No log lines.'}</pre>
    {#if logs.truncated}<p class="text-xs text-muted">Output was cut to the newest part.</p>{/if}
  {:else}
    <p class="text-sm text-muted" role="status">Loading…</p>
  {/if}
</section>
