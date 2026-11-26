<script lang="ts">
  import { api, lostConnection, probeServer } from '../../lib/api';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { shouldConfirm, watchOldAddress, webAddress } from '../../lib/svcweb';
  import type { WebPortStatus } from '../../lib/svcweb';
  import Confirm from '../Confirm.svelte';
  import Refresh from '@lucide/svelte/icons/refresh-cw';

  let st = $state<WebPortStatus | null>(null), err = $state(''), busy = $state(false);
  let askMove = $state(false), askBack = $state(false);
  // After a move the page waits for the new address and then offers the link.
  let wait = $state<{ port: number; link: string; state: 'ready' | 'returned' } | null>(null);

  async function load() {
    busy = true;
    try {
      st = await api.webPort(); err = '';
      // The page arrived on the new port: that is the confirmation.
      if (shouldConfirm(st, location)) {
        st = await api.setWebPort('confirm');
        toast('nodespot answers on the new port. The move is confirmed.', 'info');
      }
    } catch (e) { err = e instanceof Error ? e.message : String(e); } finally { busy = false; }
  }
  $effect(() => { load(); });

  async function go(action: 'move' | 'back') {
    busy = true;
    let port = action === 'move' ? 80 : (st?.backPort ?? 8080);
    try {
      st = await api.setWebPort(action);
      port = st.port;
    } catch (e) {
      // The restart can cut the answer off; anything else is a real error.
      if (!lostConnection(e)) { toastError(e); busy = false; await load(); return; }
    }
    busy = false;
    const link = webAddress(location, port, '/#/settings/system/web-port');
    wait = { port, link, state: 'ready' };
    // The old address goes away; if it comes back, the helper undid the move.
    const r = await watchOldAddress({ probe: () => probeServer(3000), timeoutMs: 100000 });
    if (wait && r === 'returned') wait = { ...wait, state: 'returned' };
  }

  const reasons: Record<string, string> = {
    busy: 'Port 80 is in use by another program, such as a web server. Stop that program first.',
    unchecked: 'Cannot check whether port 80 is free (the ss tool is missing), so the move is not offered.',
    unavailable: 'The nodespot configuration could not be read.',
  };
</script>

<p class="text-sm text-muted">nodespot listens on port 8080 by default. Moving it to port 80 lets you open it as http://{location.hostname}/ without a port number. Port 80 must be free.</p>

{#if err}
  <p class="text-sm text-danger" role="alert">{err}</p>
{:else if !st}
  <p class="text-sm text-muted" role="status">Loading…</p>
{:else if !st.available}
  <p class="text-sm text-muted">Not available: the nodespot configuration could not be read.</p>
{:else}
  <section class="card space-y-3 p-4" aria-label="Web port">
    <div class="flex items-center justify-between gap-2">
      <h3 class="text-base font-semibold">Current port: <span class="font-mono">{st.port}</span></h3>
      <button class="btn btn-sm" disabled={busy} onclick={load}><Refresh size={13} />Refresh</button>
    </div>
    <p class="font-mono text-xs text-muted">listen = {st.listen}</p>

    {#if wait}
      <div class="space-y-2 rounded border border-line p-3 text-sm" role="status">
        {#if wait.state === 'ready'}
          <p>nodespot is moving to port {wait.port}. Open the new address in a moment.</p>
          <a class="btn btn-primary" href={wait.link}>Open the new address</a>
          <p class="text-xs text-muted">{wait.link}</p>
          <p class="text-xs text-muted">The old address stops working. If the new address does not answer within 60 seconds, nodespot goes back to its old port by itself and this page comes back.</p>
        {:else}
          <p class="text-warn">The move to port {wait.port} was not confirmed and has been undone. nodespot is on its old port again.</p>
          <button class="btn" onclick={() => { wait = null; load(); }}>Continue</button>
        {/if}
      </div>
    {:else if st.pending}
      <p class="text-sm text-warn" role="status">A move to port 80 is waiting for confirmation ({st.pending.seconds} s left). Open the new address to confirm it, or nodespot goes back to its old port.</p>
      <a class="btn btn-primary" href={webAddress(location, 80, '/#/settings/system/web-port')}>Open the new address</a>
    {/if}
    {#if st.reverted && !wait}
      <p class="text-sm text-warn" role="status">The last move was not confirmed in time and was undone, so nodespot is on its old port.</p>
    {/if}

    {#if st.on80}
      <p class="text-sm">nodespot is on port 80. To free port 80 again, move nodespot back to port {st.backPort}.</p>
      <button class="btn" disabled={busy || !!wait} onclick={() => (askBack = true)}>Move back to port {st.backPort}</button>
    {:else if st.canMove}
      <p class="text-sm">Port 80 is free.</p>
      <button class="btn btn-primary" disabled={busy || !!wait} onclick={() => (askMove = true)}>Move to port 80</button>
    {:else}
      <p class="text-sm" role="note">{reasons[st.reason] ?? 'Moving to port 80 is not possible now.'}</p>
      {#if st.dropIn}<button class="btn" disabled={busy || !!wait} onclick={() => (askBack = true)}>Undo the earlier move</button>{/if}
    {/if}
    {#if st.firewall}<p class="text-xs text-muted">{st.firewall}</p>{/if}
  </section>
{/if}

<Confirm bind:open={askMove} title="Move nodespot to port 80?" message={`nodespot restarts on port 80. Old bookmarks to port ${st?.port ?? 8080} stop working. Open the new address when the page offers it; if it does not answer within 60 seconds, nodespot goes back by itself.`} confirmLabel="Move to port 80" onconfirm={() => go('move')} />
<Confirm bind:open={askBack} title="Move nodespot back?" message={`nodespot restarts on port ${st?.backPort ?? 8080}. The address without a port number stops working.`} confirmLabel="Move back" onconfirm={() => go('back')} />
