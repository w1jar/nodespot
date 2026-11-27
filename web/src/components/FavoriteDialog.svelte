<script lang="ts">
  import { api } from '../lib/api';
  import { app, refreshFavorites } from '../lib/store.svelte';
  import { toast, toastError } from '../lib/toast.svelte';
  import { modeTitle, pickerModes, targetLabel } from '../lib/modes';
  import { settings } from '../lib/settings.svelte';
  import type { Favorite } from '../lib/types';
  import Dialog from './Dialog.svelte';

  let { open = $bindable(false), fav = null, initialMode = '', initialTarget = '' }: { open: boolean; fav?: Favorite | null; initialMode?: string; initialTarget?: string } = $props();
  let mode = $state(''), target = $state(''), label = $state(''), group = $state(''), busy = $state(false);
  // Hidden modes are not offered, except the one an edited favorite (or the page that opened the dialog) already uses.
  const usable = $derived(pickerModes(app.modes, settings.hiddenModes, [fav?.mode ?? '', ...[initialMode, app.node?.active ?? ''].filter((n) => app.modes.some((m) => m.name === n && m.configured))].filter(Boolean)));
  const info = $derived(app.modes.find((m) => m.name === mode));
  const groups = $derived([...new Set(app.favorites.filter((f) => f.mode === mode).map((f) => f.group).filter(Boolean))]);
  let wasOpen = false;
  $effect(() => {
    if (open && !wasOpen) {
      mode = fav?.mode ?? (initialMode || app.node?.active || usable[0]?.name || '');
      target = fav?.target ?? initialTarget; label = fav?.label ?? ''; group = fav?.group ?? '';
    }
    wasOpen = open;
  });
  async function save(e: Event) {
    e.preventDefault();
    if (!target.trim()) { toast('Enter a target', 'error'); return; }
    busy = true;
    try {
      if (fav) await api.updateFavorite(fav.id, { target: target.trim(), label: label.trim(), group: group.trim() });
      else await api.addFavorite({ mode, target: target.trim(), label: label.trim() || undefined, group: group.trim() });
      await refreshFavorites();
      toast(fav ? 'Favorite updated' : 'Favorite added');
      open = false;
    } catch (x) { toastError(x); } finally { busy = false; }
  }
</script>
<Dialog bind:open title={fav ? 'Edit favorite' : 'Add favorite'}>
  <form id="favform" class="space-y-3" onsubmit={save}>
    <div>
      <label class="label" for="fmode">Mode</label>
      <select id="fmode" class="input" bind:value={mode} disabled={!!fav}>
        {#each usable as m (m.name)}<option value={m.name}>{m.title || modeTitle(m.name)}</option>{/each}
        {#if fav && !usable.some((m) => m.name === fav.mode)}<option value={fav.mode}>{modeTitle(fav.mode)}</option>{/if}
      </select>
    </div>
    <div>
      <label class="label" for="ftarget">{targetLabel(info)}</label>
      <input id="ftarget" class="input font-mono" bind:value={target} autocomplete="off" />
      {#if info?.targetHelp}<p class="mt-1 text-xs text-muted">{info.targetHelp}</p>{/if}
    </div>
    <div><label class="label" for="flabel">Label <span class="font-normal">(optional)</span></label><input id="flabel" class="input" bind:value={label} /></div>
    <div>
      <label class="label" for="fgroup">Group</label>
      <input id="fgroup" class="input" list="groups" bind:value={group} placeholder="No group" />
      <datalist id="groups">{#each groups as g}<option value={g}></option>{/each}</datalist>
    </div>
  </form>
  {#snippet footer()}
    <button class="btn" onclick={() => (open = false)}>Cancel</button>
    <button class="btn btn-primary" type="submit" form="favform" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>
