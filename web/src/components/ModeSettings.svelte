<script lang="ts">
  // The settings of one mode, inline on the Modes page: not-ready message, the AMBE vocoder notice with its Install button,
  // the fields as the server lists them (empty ones show the station value as placeholder), Save and Clear settings.
  import { api } from '../lib/api';
  import { app, refreshModes } from '../lib/store.svelte';
  import { toast, toastError } from '../lib/toast.svelte';
  import { draftOf, missingField, savePayload, dirty, modeTitle, notReadyText, needsVocoder, vocoderBusy, type StationValues } from '../lib/modes';
  import type { ModeInfo, Vocoder } from '../lib/types';
  import ModeFields from './ModeFields.svelte';
  import Confirm from './Confirm.svelte';

  let { info, vocoder = null, onvocoder }: { info: ModeInfo; vocoder?: Vocoder | null; onvocoder?: (v: Vocoder) => void } = $props();
  const mode = $derived(info.name);
  let draft = $state<Record<string, string>>({});
  let ids = $state<StationValues | undefined>(undefined);
  $effect(() => { api.station().then((s) => { ids = { callsign: s.callsign, dmrId: s.dmrId, nxdnId: s.nxdnId }; }).catch(() => { /* the node checks */ }); });
  let err = $state(''), busy = $state(false), seen = '';
  $effect(() => { if (seen !== info.name) { seen = info.name; draft = draftOf(info); err = ''; } });

  const hasSaved = $derived(info.saved ?? info.configured);

  async function save(ev: Event) {
    ev.preventDefault();
    const f = missingField(info, draft, ids);
    err = f ? `${f.label} is required.` : '';
    if (f) return;
    busy = true;
    try {
      await api.saveMode(mode, { values: savePayload(info.fields ?? [], draft) });
      await refreshModes();
      const fresh = app.modes.find((m) => m.name === mode);
      if (fresh) draft = draftOf(fresh);
      toast(`${modeTitle(mode, app.modes)} saved`);
    } catch (e) { toastError(e); } finally { busy = false; }
  }
  // Delete one saved password; every other setting, and anything typed but not saved, stays.
  async function clearSecret(key: string) {
    busy = true;
    try {
      await api.saveMode(mode, { clear: [key] });
      await refreshModes();
      draft[key] = '';
      toast('Password cleared');
    } catch (e) { toastError(e); } finally { busy = false; }
  }
  let clearAll = $state(false);
  // The node drops an active mode before it forgets the settings (and the remembered target).
  async function clear() {
    busy = true;
    try {
      await api.deleteMode(mode);
      await refreshModes();
      const fresh = app.modes.find((m) => m.name === mode);
      if (fresh) draft = draftOf(fresh);
      toast('Settings cleared');
    } catch (e) { toastError(e); } finally { busy = false; }
  }

  // The software AMBE vocoder (AMBE modes): installing is a helper job, the page above polls until it ends.
  let vocBusy = $state(false);
  const vocNeeded = $derived(needsVocoder(info, vocoder));
  async function installVocoder() {
    if (vocBusy) return;
    vocBusy = true;
    try { const v = await api.installVocoder(); onvocoder?.(v); if (v.job === 'running') toast('Installing the AMBE vocoder…'); } catch (e) { toastError(e); } finally { vocBusy = false; }
  }
</script>

<div class="space-y-3" data-testid="mode-settings">
  {#if !info.configured}<p class="text-sm text-warn" role="status" data-testid="not-ready">{notReadyText(info)}</p>{/if}
  {#if vocNeeded}
    <div class="flex flex-wrap items-center gap-2 rounded border border-warn/60 bg-warn/5 px-3 py-2 text-sm" role="status" data-testid="vocoder-notice">
      <span class="min-w-0 flex-1">Needs the software AMBE vocoder{#if vocoder && !vocoder.supported && vocoder.reason}. {vocoder.reason}{/if}</span>
      {#if vocoder?.supported !== false}
        <button type="button" class="btn btn-primary btn-sm" disabled={vocBusy || vocoderBusy(vocoder)} onclick={installVocoder}>{vocBusy || vocoderBusy(vocoder) ? 'Installing…' : 'Install'}</button>
      {/if}
    </div>
  {/if}
  {#if !(info.fields ?? []).length}
    <p class="text-sm text-muted">This mode has no settings.</p>
  {:else}
    <form class="space-y-3" onsubmit={save}>
      <ModeFields mode={info} bind:draft idPrefix="mp" {busy} onclear={clearSecret} />
      {#if err}<p class="text-sm text-danger" role="alert">{err}</p>{/if}
      <div class="flex flex-wrap gap-2">
        <button class="btn btn-primary" type="submit" disabled={busy || !dirty(info, draft)}>{busy ? 'Saving…' : 'Save'}</button>
        <button class="btn btn-bad-outline" type="button" onclick={() => (clearAll = true)} disabled={busy || !hasSaved} title={hasSaved ? '' : 'Nothing is saved for this mode'} data-testid="clear-settings">Clear settings</button>
      </div>
    </form>
  {/if}
  <Confirm bind:open={clearAll} title="Clear {info.title || info.name} settings?" message={info.active ? `${info.title || info.name} is active. It disconnects first, then all of its saved settings, passwords and its remembered target are removed.` : `All saved settings, passwords and the remembered target of ${info.title || info.name} are removed.`} confirmLabel="Clear settings" onconfirm={clear} />
</div>
