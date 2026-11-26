<script lang="ts">
  // Audio and timers: the AGC both ways, the hang and transmit timeout, the courtesy tone and the station ID's timing and
  // sound. All are in station.advanced and station.id; the node applies a save at once, with no restart. Empty means the default.
  import { api } from '../../lib/api';
  import { refreshNode } from '../../lib/store.svelte';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { AGC_DEFAULTS, AGC_FIELDS, ID_FIELDS, TIMER_FIELDS, audioBody, audioDirty, audioError } from '../../lib/audio';
  import type { NumField } from '../../lib/audio';
  import type { Station, StationAdvanced, StationId } from '../../lib/types';

  let { onsaved }: { onsaved?: () => void } = $props();
  let saved = $state<Station | null>(null), adv = $state<StationAdvanced>({}), id = $state<StationId>({});
  let err = $state(''), busy = $state(false);
  const chk = 'mt-0.5 size-4 accent-[var(--accent)]';
  const dirty = $derived(!!saved && audioDirty(saved, adv, id));

  function fill(s: Station) { saved = s; adv = { ...s.advanced }; id = { ...s.id }; err = ''; }
  async function load() { try { fill(await api.station()); } catch (e) { toastError(e); } }
  $effect(() => { load(); });

  // The switches show the default when unset.
  const modeAgc = $derived(adv.modeAgc ?? AGC_DEFAULTS.modeAgc);
  const radioAgc = $derived(adv.radioAgc ?? AGC_DEFAULTS.radioAgc);
  const courtesy = $derived(adv.courtesyTone ?? AGC_DEFAULTS.courtesyTone);
  const idOn = $derived(id.enabled !== false);

  async function save(ev: Event) {
    ev.preventDefault();
    if (!saved) return;
    err = audioError(adv, id);
    if (err) return;
    busy = true;
    try {
      // Start from the station as saved now, so other settings changed since the page opened are kept.
      const fresh = await api.station();
      await api.saveStation(audioBody(fresh, adv, id));
      await refreshNode(); await load(); onsaved?.(); toast('Audio and timers saved');
    } catch (e) { toastError(e); } finally { busy = false; }
  }
</script>

{#snippet num(f: NumField, o: Record<string, unknown>, p: string)}
  <div>
    <label class="label" for="{p}-{f.key}">{f.label} ({f.unit})</label>
    <input id="{p}-{f.key}" class="input tabular-nums" type="number" inputmode="numeric" min={f.min} max={f.max} step={f.step} bind:value={o[f.key] as number | undefined} placeholder={String(f.def)} />
    <p class="mt-1 text-xs text-muted">{f.help}</p>
  </div>
{/snippet}

<section class="card mt-4 space-y-4 p-4" aria-label="Audio and timers">
  <div class="flex flex-wrap items-center gap-2">
    <h2 class="text-base font-semibold">Audio and timers</h2>
    {#if dirty}<span class="badge" role="status">Unsaved changes</span>{/if}
  </div>
  {#if saved}
    <form class="space-y-4" onsubmit={save}>
      <fieldset class="space-y-3">
        <legend class="text-sm font-medium">Automatic gain (AGC)</legend>
        <div class="grid gap-2 sm:grid-cols-2">
          <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={modeAgc} onchange={(e) => (adv.modeAgc = e.currentTarget.checked)} /><span>Network to radio<span class="block text-xs text-muted">Levels what the active mode sends to the transmitter. On by default.</span></span></label>
          <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={radioAgc} onchange={(e) => (adv.radioAgc = e.currentTarget.checked)} /><span>Radio to network<span class="block text-xs text-muted">Levels what the receiver sends to the mode. Off by default.</span></span></label>
        </div>
        <div class="grid gap-3 sm:grid-cols-2">{#each AGC_FIELDS as f (f.key)}{@render num(f, adv, 'at')}{/each}</div>
        <p class="text-xs text-muted">The target and maximum gain apply to both directions.</p>
      </fieldset>

      <fieldset class="space-y-3">
        <legend class="text-sm font-medium">Timers</legend>
        <div class="grid gap-3 sm:grid-cols-2">{#each TIMER_FIELDS as f (f.key)}{@render num(f, adv, 'at')}{/each}</div>
        <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={courtesy} onchange={(e) => (adv.courtesyTone = e.currentTarget.checked)} /><span>Courtesy tone<span class="block text-xs text-muted">A short beep after a local transmission. On by default.</span></span></label>
      </fieldset>

      <fieldset class="space-y-3">
        <legend class="text-sm font-medium">Station ID</legend>
        <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={idOn} onchange={(e) => (id.enabled = e.currentTarget.checked)} /><span>Identify the station<span class="block text-xs text-muted">Sent on the radio only, after activity. The licensee is responsible for IDing.</span></span></label>
        <div class="grid gap-3 sm:grid-cols-3">{#each ID_FIELDS as f (f.key)}{@render num(f, id, 'at-id')}{/each}</div>
        <div>
          <label class="label" for="at-voice">Voice file</label>
          <input id="at-voice" class="input font-mono" bind:value={id.voiceFile} placeholder="empty: CW" autocomplete="off" spellcheck="false" />
          <p class="mt-1 text-xs text-muted">Absolute path of an 8 kHz mono WAV played instead of CW. If it cannot be read, the node sends CW.</p>
        </div>
      </fieldset>

      {#if err}<p class="text-sm text-danger" role="alert">{err}</p>{/if}
      <div class="flex flex-wrap items-center gap-2">
        <button class="btn btn-primary" type="submit" disabled={busy || !dirty}>{busy ? 'Saving…' : 'Save'}</button>
        <button class="btn btn-accent-outline" type="button" disabled={busy || !dirty} onclick={() => saved && fill(saved)}>Revert</button>
        <p class="text-xs text-muted">Applies at once. No restart needed. Empty fields use the default shown.</p>
      </div>
    </form>
  {:else}<p class="text-sm text-muted">Loading…</p>{/if}
</section>
