<script lang="ts">
  // The nodespot-node systemd unit: the process that holds the radio and the one active mode.
  import { api } from '../../lib/api';
  import { app } from '../../lib/store.svelte';
  import { toast, toastError } from '../../lib/toast.svelte';
  import type { NodeService } from '../../lib/types';
  import Confirm from '../Confirm.svelte';

  let ns = $state<NodeService | null>(null), busy = $state(false), ask = $state(false);
  async function load() { try { ns = await api.nodeService(); } catch (e) { toastError(e); } }
  $effect(() => { load(); });
  async function restart() {
    busy = true;
    try { await api.restartNodeService(); toast('Node restarting'); setTimeout(load, 3000); } catch (e) { toastError(e); } finally { busy = false; }
  }
</script>

<section class="space-y-3" aria-label="Node service">
  {#if ns?.service}
    <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
      <dt class="text-muted">Unit</dt><dd class="font-mono">{ns.service.unit}</dd>
      <dt class="text-muted">State</dt><dd>{ns.service.active} ({ns.service.sub})</dd>
      <dt class="text-muted">At boot</dt><dd>{ns.service.enabled}</dd>
      {#if ns.service.since}<dt class="text-muted">Since</dt><dd>{ns.service.since}</dd>{/if}
    </dl>
  {:else if ns}
    <p class="text-sm text-muted">The node service cannot be managed from here.</p>
  {:else}<p class="text-sm text-muted">Loading…</p>{/if}
  <p class="text-sm text-muted">The node is {app.node?.running ? 'running' : 'not running'}{#if app.node?.error}: {app.node.error}{/if}.</p>
  <button class="btn" onclick={() => (ask = true)} disabled={busy || !ns?.available}>Restart node</button>
</section>
<Confirm bind:open={ask} title="Restart the node?" message="The active mode disconnects and the radio is released for a few seconds." confirmLabel="Restart" onconfirm={restart} />
