<script lang="ts">
  import { api } from '../lib/api';
  import { toast, toastError } from '../lib/toast.svelte';
  import type { BackupInfo } from '../lib/types';
  import RestoreFlow from './RestoreFlow.svelte';
  import Download from '@lucide/svelte/icons/download';

  let binfo = $state<BackupInfo | null>(null), secrets = $state(true), bbusy = $state(false), restoring = $state(false);
  async function loadBackup() { try { binfo = await api.backupInfo(); secrets = binfo.secrets; } catch (e) { toastError(e); } }
  async function downloadBackup() {
    bbusy = true;
    try {
      const name = await api.backup({ secrets: secrets && !!binfo?.secrets });
      toast(`Saved ${name}. Store it like a password.`, 'info');
    } catch (e) { toastError(e); } finally { bbusy = false; }
  }
  $effect(() => { loadBackup(); });
</script>
<div class="space-y-4">
  <section class="card space-y-3 p-4" aria-label="Backup">
    <p class="text-sm">Download one file with this node's configuration: the station, the modes, nodespot users, favorites and schedules. Restore it onto a freshly installed node to get the same node back.</p>
    <p class="text-sm text-muted">Always included: the node configuration and the nodespot database{#if binfo?.sa818}, and the SA818 radio module settings{/if}.</p>
    {#if binfo?.secrets}
      <label class="flex cursor-pointer items-center gap-2 text-sm"><input type="checkbox" class="size-4 accent-[var(--accent)]" bind:checked={secrets} disabled={bbusy} />Include the mode passwords</label>
    {/if}
    <div class="rounded border border-warn bg-warn/10 p-3 text-sm" role="note">
      <strong class="text-warn">The backup file is NOT encrypted.</strong> It can contain the nodespot password hashes{#if secrets && binfo?.secrets} and the passwords of the modes (AllStar, EchoLink, DMR masters){/if}. Store it like a password and never share it.
    </div>
    <div class="flex flex-wrap gap-2">
      <button class="btn btn-primary" disabled={bbusy || !binfo} onclick={downloadBackup}><Download size={14} class={bbusy ? 'animate-pulse' : ''} />{bbusy ? 'Preparing…' : 'Download backup'}</button>
    </div>
    <div class="border-t border-line pt-3">
      <h3 class="text-sm font-semibold">Restore from a backup</h3>
      {#if restoring}
        <div class="mt-2"><RestoreFlow mode="admin" oncancel={() => (restoring = false)} /></div>
      {:else}
        <p class="mt-1 text-sm text-muted">Replace this node's configuration with a backup. You will see a preview first.</p>
        <button class="btn mt-2" onclick={() => (restoring = true)}>Restore from a backup…</button>
      {/if}
    </div>
  </section>
</div>
