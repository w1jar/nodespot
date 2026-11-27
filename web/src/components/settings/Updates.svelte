<script lang="ts">
  import { untrack } from 'svelte';
  import { api, lostConnection } from '../../lib/api';
  import { app, awaitRestart } from '../../lib/store.svelte';
  import { toast, toastError } from '../../lib/toast.svelte';
  import type { UpdateInfo } from '../../lib/types';
  import { parseNotes, fmtAgoLong } from '../../lib/util';
  import Confirm from '../Confirm.svelte';
  import Dialog from '../Dialog.svelte';
  import Refresh from '@lucide/svelte/icons/refresh-cw';
  import Download from '@lucide/svelte/icons/download';
  import Power from '@lucide/svelte/icons/power';
  import ShieldAlert from '@lucide/svelte/icons/shield-alert';

  // nodespot
  let upd = $state<UpdateInfo | null>(null), ubusy2 = $state(false), updOpen = $state(false), updErr = $state('');
  let reconnecting = $state(false), noopMsg = $state(false), nowMs = $state(Date.now());
  const checkedText = $derived(upd?.checked ? fmtAgoLong((nowMs - new Date(upd.checked).getTime()) / 1000) : 'not checked yet');
  $effect(() => { const t = setInterval(() => (nowMs = Date.now()), 15000); return () => clearInterval(t); });
  const noopText = 'Already up to date (the package index may lag a few minutes; try again later).';
  const notes = $derived(upd?.notes ? parseNotes(upd.notes) : []);
  async function loadUpd() { try { upd = await api.updates(); updErr = ''; } catch (e) { updErr = e instanceof Error ? e.message : String(e); } }
  async function checkUpd() {
    ubusy2 = true; noopMsg = false;
    try { upd = await api.checkUpdates(); updErr = ''; nowMs = Date.now(); toast(upd.upgradable ? `Version ${upd.candidate} is available` : 'nodespot is up to date', 'info'); } catch (e) { toastError(e); } finally { ubusy2 = false; }
  }
  async function setChannel(channel: 'stable' | 'beta') {
    if (!upd || channel === upd.channel) return;
    ubusy2 = true; noopMsg = false;
    try { upd = await api.setUpdateChannel(channel); updErr = ''; nowMs = Date.now(); toast(channel === 'beta' ? 'Update channel: Beta' : 'Update channel: Release', 'info'); } catch (e) { toastError(e); await loadUpd(); } finally { ubusy2 = false; }
  }
  async function applyUpd() {
    if (!upd) return;
    ubusy2 = true; noopMsg = false;
    try {
      const target = upd.candidate;
      upd = await api.applyUpdate();
      app.updating = { from: app.version, target };
    } catch (e) { toastError(e); } finally { ubusy2 = false; }
  }
  // While updating: poll the job. The server restarts during the upgrade, so
  // failures here are expected; the store reloads the page when the new
  // version says hello.
  $effect(() => {
    if (!app.updating) { reconnecting = false; return; }
    let alive = true, doneAt = 0;
    const tick = async () => {
      try {
        const u = await api.updates();
        if (!alive) return;
        // The helper restarts during the upgrade: a brief 'unavailable' answer
        // (applyReason set) is part of the update, not a failure. Keep the
        // last state and wait.
        if (!u.available || u.applyReason) { reconnecting = true; return; }
        upd = u; reconnecting = false;
        if (u.job === 'noop') { app.updating = null; noopMsg = true; toast(noopText, 'info'); }
        else if (u.job === 'failed') { app.updating = null; toast('Update failed. See the log below.', 'error'); }
        else if (u.job === 'succeeded') {
          doneAt ||= Date.now();
          if (Date.now() - doneAt > 30000) { app.updating = null; toast(`Update to ${u.installed} installed. If nodespot did not restart, restart the nodespot service.`, 'info'); }
        }
      } catch { if (alive) reconnecting = true; }
    };
    const t = setInterval(tick, 2000);
    return () => { alive = false; clearInterval(t); };
  });
  // A job can be running without this page having started it: after the
  // upgrade restarts the server the page reloads while the job is still
  // finishing (or another admin started it). Keep polling until it ends.
  $effect(() => {
    if (app.updating || upd?.job !== 'running') return;
    let alive = true;
    const t = setInterval(async () => {
      try {
        const u = await api.updates();
        if (!alive || !u.available || u.applyReason) return;
        upd = u;
        if (u.job === 'noop') noopMsg = true;
        else if (u.job === 'failed') toast('Update failed. See the log below.', 'error');
      } catch { /* server restarting; try again */ }
    }, 2000);
    return () => { alive = false; clearInterval(t); };
  });

  // System packages (apt)
  const os = $derived(upd?.os);
  let osBusy = $state(false), osOpen = $state(false), rebootOpen = $state(false), osDone = $state(false);
  const osChecked = $derived(os?.checkedAt ? fmtAgoLong((nowMs - new Date(os.checkedAt).getTime()) / 1000) : 'not checked yet');
  const secCount = $derived(os ? os.packages.filter((p) => p.security).length : 0);
  const hasNodespotPkg = $derived(!!os?.packages.some((p) => p.name === 'nodespot'));
  const osRunning = $derived(os?.job === 'running');
  const aptBusy = $derived(osRunning || upd?.job === 'running' || !!app.updating);
  async function osCheck() {
    osBusy = true; osDone = false;
    try { const u = await api.osCheck(); if (u) upd = u; else await loadUpd(); nowMs = Date.now(); } catch (e) { toastError(e); } finally { osBusy = false; }
  }
  async function osApply() {
    osBusy = true; osDone = false;
    try {
      const restartsPanel = hasNodespotPkg;
      const u = await api.osApply(); if (u) upd = u; else await loadUpd();
      if (restartsPanel) toast('The panel restarts during this update; this page reloads when it is back', 'info');
    } catch (e) { toastError(e); await loadUpd(); } finally { osBusy = false; }
  }
  async function doReboot() {
    const before = app.instance;
    try { await api.reboot(); awaitRestart('reboot', before); } catch (e) { if (lostConnection(e)) awaitRestart('reboot', before); else toastError(e); }
  }
  // Poll while a check or an upgrade runs (also after a reload mid-job). The
  // panel may restart under an upgrade; failures just mean "try again".
  $effect(() => {
    if (!os || !(os.checking || os.job === 'running')) return;
    let alive = true, was = os.job;
    const t = setInterval(async () => {
      try {
        const u = await api.updates();
        if (!alive) return;
        upd = u; nowMs = Date.now();
        if (was === 'running' && u.os && u.os.job !== 'running') {
          if (u.os.job === 'failed') toast('System update failed. See the log below.', 'error'); else osDone = true;
        }
        was = u.os?.job ?? was;
      } catch { /* server restarting; try again */ }
    }, 2000);
    return () => { alive = false; clearInterval(t); };
  });
  // Another admin's check or the daily check changes counts: refresh.
  $effect(() => { app.updates.os; app.updates.security; app.updates.reboot; app.updates.nodespot; untrack(() => { loadUpd(); }); });
</script>
<div class="space-y-4">
  <div class="grid grid-cols-1 items-start gap-4 lg:grid-cols-2">
  <section class="card space-y-3 p-4" aria-label="nodespot">
    <h2 class="text-base font-semibold">nodespot</h2>
    {#if !upd}
      <p class="text-sm text-muted">{updErr || 'Loading…'}</p>
    {:else if !upd.available}
      <p class="text-sm text-muted">Updates from the web interface are not available: {upd.reason ?? 'unknown reason'}.</p>
      <p class="text-xs text-muted">Install nodespot from its apt repository to get one-click updates.</p>
    {:else}
      <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <dt class="text-muted">Installed</dt><dd class="font-mono">{upd.installed}</dd>
        <dt class="text-muted">Available</dt><dd class="font-mono">{upd.upgradable ? upd.candidate : 'up to date'}</dd>
        <dt class="text-muted">Last checked</dt><dd title={upd.checked ? new Date(upd.checked).toLocaleString() : ''}>{checkedText}</dd>
      </dl>
      <div>
        <label class="label" for="upd-channel">Update channel</label>
        <select id="upd-channel" class="input w-auto" value={upd.channel} disabled={ubusy2 || !!app.updating || upd.job === 'running' || !!upd.applyReason} onchange={(e) => setChannel(e.currentTarget.value === 'beta' ? 'beta' : 'stable')}>
          <option value="stable">Release</option>
          <option value="beta">Beta</option>
        </select>
        <p class="mt-1 text-xs text-muted">Beta gets new versions as soon as they are built and may be less stable. Release only gets versions promoted after testing.</p>
        {#if upd.channel === 'stable' && upd.installed.includes('~')}
          <p class="mt-1 text-xs text-muted" role="status">A beta version is installed. Switching to Release does not downgrade: this node stays on {upd.installed} until a release newer than it comes out.</p>
        {/if}
      </div>
      {#if upd.upgradable && notes.length}
        <div class="rounded border border-line bg-surface-2/40 p-3 text-sm" aria-label="Release notes">
          {#each notes as b}
            {#if b.kind === 'ul'}
              <ul class="list-disc space-y-0.5 pl-5">{#each b.items as it}<li>{#each it as seg}{#if seg.bold}<strong>{seg.text}</strong>{:else}{seg.text}{/if}{/each}</li>{/each}</ul>
            {:else}
              <p class="[&:not(:first-child)]:mt-2">{#each b.lines as ln, i}{#if i}<br />{/if}{#each ln as seg}{#if seg.bold}<strong>{seg.text}</strong>{:else}{seg.text}{/if}{/each}{/each}</p>
            {/if}
          {/each}
        </div>
      {/if}
      {#if noopMsg && !app.updating && upd.job === 'noop'}
        <p class="text-sm" role="status">{noopText}</p>
      {/if}
      {#if upd.applyReason}
        <p class="text-sm text-muted" role="status">Updating from this page is not possible right now: {upd.applyReason}.</p>
      {/if}
      {#if app.updating}
        <p class="flex items-center gap-2 text-sm" role="status"><Refresh size={14} class="animate-spin" />{reconnecting || app.conn !== 'live' ? 'Updating… reconnecting' : 'Updating…'}</p>
      {:else if upd.job === 'running'}
        <p class="flex items-center gap-2 text-sm" role="status"><Refresh size={14} class="animate-spin" />An update is running…</p>
      {/if}
      {#if upd.jobLog && (app.updating || upd.job === 'failed' || upd.job === 'running')}
        <pre class="max-h-48 overflow-auto rounded border border-line bg-surface-2/40 p-2 text-xs whitespace-pre-wrap" aria-label="Update log">{upd.jobLog}</pre>
      {/if}
      <div class="flex flex-wrap gap-2">
        <button class="btn" disabled={ubusy2 || !!app.updating} onclick={checkUpd}><Refresh size={14} class={ubusy2 ? 'animate-spin' : ''} />Check now</button>
        {#if upd.upgradable}
          <button class="btn btn-primary" disabled={ubusy2 || !!app.updating || upd.job === 'running' || !!upd.applyReason} onclick={() => (updOpen = true)}><Download size={14} />Update to {upd.candidate}</button>
        {/if}
      </div>
    {/if}
  </section>

  <section class="card space-y-3 p-4" aria-label="System packages">
    <h2 class="text-base font-semibold">System packages</h2>
    {#if !upd}
      <p class="text-sm text-muted">{updErr || 'Loading…'}</p>
    {:else if !os || !os.available}
      <p class="text-sm text-muted">System updates from the web interface are not available{os?.reason ? `: ${os.reason}` : ''}.</p>
    {:else}
      <p class="text-sm">
        {#if os.checking}<span class="inline-flex items-center gap-2"><Refresh size={14} class="animate-spin" />Checking for updates…</span>
        {:else if os.packages.length}<b>{os.packages.length}</b> package{os.packages.length === 1 ? '' : 's'} can be upgraded{#if secCount}, <span class="text-warn">{secCount} security</span>{/if}.
        {:else if os.checkedAt}All packages are up to date.{:else}Not checked yet.{/if}
        <span class="text-muted" title={os.checkedAt ? new Date(os.checkedAt).toLocaleString() : ''}>Last checked: {osChecked}.</span>
      </p>
      {#if os.error}<p class="text-sm text-danger" role="alert">{os.error}</p>{/if}
      {#each os.warnings ?? [] as w}<p class="text-sm text-warn" role="status">{w}</p>{/each}
      {#if os.rebootRequired}
        <div class="flex flex-wrap items-center gap-3 rounded border border-warn/60 p-3 text-sm" role="status">
          <ShieldAlert size={16} class="text-warn" />
          <span class="min-w-0 flex-1">A reboot is required{#if os.rebootPkgs.length} ({os.rebootPkgs.join(', ')}){/if}. The radio node and its link go down while the system restarts.</span>
          <button class="btn btn-danger" disabled={osRunning} onclick={() => (rebootOpen = true)}><Power size={14} />Reboot now</button>
        </div>
      {/if}
      {#if os.packages.length}
        <ul class="max-h-72 divide-y divide-line overflow-auto rounded border border-line text-xs" aria-label="Upgradable packages">
          {#each os.packages as p}
            <li class="flex flex-wrap items-baseline gap-x-2 px-2 py-1"><span class="font-mono font-semibold">{p.name}</span><span class="font-mono text-muted">{p.from} → {p.to}</span>{#if p.security}<span class="badge text-warn">security</span>{/if}</li>
          {/each}
        </ul>
      {/if}
      {#if os.keptBack.length}
        <p class="text-xs text-muted">Kept back (needs a manual <span class="font-mono">apt full-upgrade</span>, not installed here): <span class="font-mono">{os.keptBack.join(', ')}</span></p>
      {/if}
      {#if osRunning}
        <p class="flex items-center gap-2 text-sm" role="status"><Refresh size={14} class="animate-spin" />Installing updates…</p>
      {:else if osDone && os.job === 'succeeded'}
        <p class="text-sm text-ok" role="status">System updates installed.</p>
      {/if}
      {#if os.jobLog && (osRunning || os.job === 'failed' || osDone)}
        <pre class="max-h-48 overflow-auto rounded border border-line bg-surface-2/40 p-2 text-xs whitespace-pre-wrap" aria-label="System update log">{os.jobLog}</pre>
      {/if}
      <div class="flex flex-wrap gap-2">
        <button class="btn" disabled={osBusy || os.checking || aptBusy} onclick={osCheck}><Refresh size={14} class={os.checking ? 'animate-spin' : ''} />Check now</button>
        {#if os.packages.length}
          <button class="btn btn-primary" disabled={osBusy || os.checking || aptBusy} onclick={() => (osOpen = true)}><Download size={14} />Install {os.packages.length} update{os.packages.length === 1 ? '' : 's'}</button>
        {/if}
      </div>
    {/if}
  </section>
  </div>
</div>
<Confirm bind:open={updOpen} title="Update nodespot?" message="Update to {upd?.candidate}? nodespot will restart; the active link drops for a moment." confirmLabel="Update" onconfirm={applyUpd} />

<Dialog bind:open={osOpen} title="Install {os?.packages.length ?? 0} system update{os?.packages.length === 1 ? '' : 's'}?">
  <div class="space-y-3 text-sm">
    {#if hasNodespotPkg}
      <p class="rounded border border-warn/60 p-2 text-warn" role="alert">The nodespot package is in this list. The panel restarts for a few seconds; this page reloads by itself.</p>
    {/if}
    <ul class="max-h-60 divide-y divide-line overflow-auto rounded border border-line text-xs" aria-label="Packages to upgrade">
      {#each os?.packages ?? [] as p}
        <li class="flex flex-wrap items-baseline gap-x-2 px-2 py-1"><span class="font-mono font-semibold">{p.name}</span><span class="font-mono text-muted">{p.from} → {p.to}</span>{#if p.security}<span class="badge text-warn">security</span>{/if}</li>
      {/each}
    </ul>
    {#if os?.keptBack.length}<p class="text-xs text-muted">Not included (kept back): {os.keptBack.join(', ')}.</p>{/if}
    <p class="text-xs text-muted">Runs a plain <span class="font-mono">apt-get upgrade</span>; existing configuration files are kept.</p>
  </div>
  {#snippet footer()}
    <button class="btn" onclick={() => (osOpen = false)}>Cancel</button>
    <button class="btn btn-primary" onclick={() => { osOpen = false; osApply(); }}>Install updates</button>
  {/snippet}
</Dialog>
<Confirm bind:open={rebootOpen} title="Reboot the node?" message="The node and its link go down; the panel comes back after the system starts." confirmLabel="Reboot now" onconfirm={doReboot} />
