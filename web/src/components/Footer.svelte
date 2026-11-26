<script lang="ts">
  import { app, isAdmin, awaitRestart } from '../lib/store.svelte';
  import { api, lostConnection } from '../lib/api';
  import { toast, toastError } from '../lib/toast.svelte';
  import { cToF, fmtUptime, bugReportUrl } from '../lib/util';
  import Confirm from './Confirm.svelte';
  const s = $derived(app.system);
  let restartNode = $state(false), restartNodespot = $state(false);
  async function doRestartNode() {
    try { await api.restartNodeService(); toast('The node is restarting; the active mode reconnects when it is up', 'info'); } catch (e) { toastError(e); }
  }
  async function doRestartNodespot() {
    const before = app.instance;
    try { await api.restartNodespot(); awaitRestart('nodespot', before); } catch (e) { if (lostConnection(e)) awaitRestart('nodespot', before); else toastError(e); }
  }
</script>
<footer class="flex flex-wrap items-center gap-x-4 gap-y-1.5 border-t border-line pt-2 text-sm" aria-label="System status">
  {#if s}
    <span title="CPU temperature">CPU Temp: <b class="font-semibold tabular-nums">{#if s.cpuTemp != null}{Math.round(cToF(s.cpuTemp))}°F / {Math.round(s.cpuTemp)}°C{:else}n/a{/if}</b></span>
    <span title="Load average 1/5/15 min">Load: <span class="tabular-nums">{s.load.map((l) => l.toFixed(2)).join(' ')}</span></span>
    <span title="Host uptime">Up: <span class="tabular-nums">{fmtUptime(s.uptime)}</span></span>
    <span class={s.node.running ? '' : 'text-danger'} title={s.node.error ?? ''}>Node: {s.node.running ? 'running' : 'down'}</span>
  {:else}<span class="text-muted">Loading system status…</span>{/if}
  <a class="text-xs text-muted underline hover:text-fg" href={bugReportUrl({ version: app.version })} target="_blank" rel="noopener noreferrer">Report a bug</a>
  {#if isAdmin()}
    <span class="ml-auto flex gap-2">
      <button class="btn btn-sm btn-danger" disabled={!!app.restarting} onclick={() => (restartNodespot = true)}>Restart nodespot</button>
      <button class="btn btn-sm btn-danger" onclick={() => (restartNode = true)}>Restart node</button>
    </span>
  {/if}
</footer>
<Confirm bind:open={restartNode} title="Restart the node?" message="The node daemon restarts. The active mode drops and reconnects once it is up. This page reconnects automatically." confirmLabel="Restart node" onconfirm={doRestartNode} />
<Confirm bind:open={restartNodespot} title="Restart nodespot?" message="Restart nodespot? The panel will be unavailable for a few seconds." confirmLabel="Restart nodespot" onconfirm={doRestartNodespot} />
