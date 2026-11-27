<script lang="ts">
  import { onMount } from 'svelte';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import { api } from '../../lib/api';
  import { isAdmin } from '../../lib/store.svelte';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { listRows, refreshWait, whenText } from '../../lib/hostlists';
  import type { HostListsStatus } from '../../lib/types';

  // The lists nodespot downloads: the AllStar node list and the digital voice host lists (DMR masters, YSF, P25, NXDN, M17).
  let st = $state<HostListsStatus | null>(null), loadErr = $state(''), busy = $state(false), now = $state(Date.now());
  const admin = $derived(isAdmin());
  const rows = $derived(listRows(st));
  const wait = $derived(refreshWait(st?.nextManual, now));
  const running = $derived(busy || !!st?.busy || !!st?.nodelist?.busy);

  async function load() { try { st = await api.hostListsStatus(); loadErr = ''; } catch (e) { loadErr = e instanceof Error ? e.message : String(e); } }
  onMount(() => {
    load();
    const t = setInterval(() => { now = Date.now(); }, 1000);
    return () => clearInterval(t);
  });
  // While a download runs, look again every few seconds.
  $effect(() => {
    if (!st?.busy && !st?.nodelist?.busy) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  });
  async function refresh() {
    busy = true;
    try {
      st = await api.refreshHostLists();
      const failed = rows.filter((r) => r.error).length;
      toast(failed ? 'Refreshed, but not every list downloaded' : 'Lists refreshed', failed ? 'error' : 'ok');
    } catch (e) { toastError(e); await load(); } finally { busy = false; now = Date.now(); }
  }
</script>

<section class="card space-y-3 p-4" aria-label="Host lists" data-testid="hostlists-card">
  <div class="flex flex-wrap items-center gap-2">
    <h2 class="min-w-0 flex-1 text-base font-semibold">Host lists</h2>
    {#if admin}
      <button class="btn btn-primary btn-sm" type="button" disabled={running} onclick={refresh} data-testid="hostlists-refresh" title="Download all lists now">
        <RefreshCw size={14} class={running ? 'animate-spin' : ''} aria-hidden="true" />{running ? 'Refreshing…' : 'Refresh'}
      </button>
    {/if}
  </div>
  <p class="text-xs text-muted">The ASL node list and the reflector and master lists the Modes page offers. They download by themselves about once a day.{#if wait > 0}{' '}The next manual refresh is allowed in {Math.ceil(wait / 60)} min.{/if}</p>
  {#if st}
    <ul class="divide-y divide-line" data-testid="hostlists-rows">
      {#each rows as r (r.id)}
        <li class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 py-2 text-sm" data-testid="hostlist-{r.id}">
          <span class="min-w-0 flex-1 font-medium">{r.label}</span>
          <span class="tabular-nums">{r.count.toLocaleString()}</span>
          <span class="text-xs text-muted">{r.updated ? `updated ${whenText(r.updated)}` : 'not downloaded yet'}</span>
          {#if r.error}<span class="basis-full break-words text-xs text-danger" role="status">{r.error}</span>{/if}
        </li>
      {/each}
    </ul>
  {:else}<p class="text-sm text-muted">{loadErr || 'Loading…'}</p>{/if}
</section>
