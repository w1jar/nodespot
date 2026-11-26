<script lang="ts">
  // Audio and timers: static gain, emphasis and filters, the AGC both ways, the timers and delays, and the station ID. All are in
  // station.advanced, station.id and (SA818 filters) station.radio; the node applies a save at once, with no restart, except that
  // the SA818 filters reach the radio only when Program is pressed. Empty means the default.
  import { api } from '../../lib/api';
  import { refreshNode } from '../../lib/store.svelte';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { AGC_DEFAULTS, AGC_FIELDS, DELAY_FIELDS, GAIN_FIELDS, ID_FIELDS, SA818_FILTER_KEYS, TIMER_FIELDS, audioBody, audioDirty, audioError, effectivePL, programmedText } from '../../lib/audio';
  import type { NumField } from '../../lib/audio';
  import { hasSA818, isShari, resolveInterface } from '../../lib/radio';
  import type { RadioInfo, Sa818Filter, Station, StationAdvanced, StationId } from '../../lib/types';

  let { onsaved }: { onsaved?: () => void } = $props();
  let saved = $state<Station | null>(null), adv = $state<StationAdvanced>({}), id = $state<StationId>({}), filt = $state<Sa818Filter>({});
  let info = $state<RadioInfo | null>(null);
  let err = $state(''), busy = $state(false);
  const chk = 'mt-0.5 size-4 accent-[var(--accent)]';
  const dirty = $derived(!!saved && audioDirty(saved, adv, id, filt));
  // Without the radio info (an old node) the SA818 port decides, as in the radio card.
  const sa818 = $derived(saved ? (info ? hasSA818(resolveInterface(saved.radio, info.presets, isShari(info))) : !!saved.radio.sa818Port) : false);
  const plOn = $derived(effectivePL(adv, sa818));

  function fill(s: Station) { saved = s; adv = { ...s.advanced }; id = { ...s.id }; filt = { ...s.radio?.sa818Filter }; err = ''; }
  async function load() {
    try { fill(await api.station()); } catch (e) { toastError(e); }
    try { info = await api.radioInfo(); } catch { /* an old node has no info */ }
  }
  $effect(() => { load(); });

  // The switches show the default when unset.
  const modeAgc = $derived(adv.modeAgc ?? AGC_DEFAULTS.modeAgc);
  const radioAgc = $derived(adv.radioAgc ?? AGC_DEFAULTS.radioAgc);
  const idOn = $derived(id.enabled !== false);
  const flagOn = (k: (typeof SA818_FILTER_KEYS)[number]) => filt[k] !== false;
  const FLAG_LABELS = { emphasis: 'Emphasis', highPass: 'High-pass', lowPass: 'Low-pass' } as const;

  async function save(ev: Event) {
    ev.preventDefault();
    if (!saved) return;
    err = audioError(adv, id);
    if (err) return;
    busy = true;
    try {
      // Start from the station as saved now, so other settings changed since the page opened are kept.
      const fresh = await api.station();
      await api.saveStation(audioBody(fresh, adv, id, sa818 ? filt : undefined));
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
        <legend class="text-sm font-medium">Gain</legend>
        <div class="grid gap-3 sm:grid-cols-2">{#each GAIN_FIELDS as f (f.key)}{@render num(f, adv, 'at')}{/each}</div>
      </fieldset>

      <fieldset class="space-y-3">
        <legend class="text-sm font-medium">Emphasis and filters</legend>
        {#if sa818}
          <div class="grid gap-2 sm:grid-cols-3">
            {#each SA818_FILTER_KEYS as k (k)}
              <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={flagOn(k)} onchange={(e) => (filt[k] = e.currentTarget.checked)} /><span>{FLAG_LABELS[k]}<span class="block text-xs text-muted">SA818 filter. On by default.</span></span></label>
            {/each}
          </div>
          <p class="text-xs text-muted">Sent to the radio when you press Program. Saving alone does not change the radio.</p>
          <p class="text-xs text-muted" data-testid="sa818-programmed">{programmedText(info?.sa818Filters)}</p>
        {:else}
          <div class="grid gap-2 sm:grid-cols-2">
            <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={!!adv.preEmphasis} onchange={(e) => (adv.preEmphasis = e.currentTarget.checked)} /><span>Pre-emphasis (transmit)<span class="block text-xs text-muted">Software 75 µs boost of high frequencies to the radio. Off by default.</span></span></label>
            <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={!!adv.deEmphasis} onchange={(e) => (adv.deEmphasis = e.currentTarget.checked)} /><span>De-emphasis (receive)<span class="block text-xs text-muted">Software 75 µs cut of high frequencies from the radio. Off by default.</span></span></label>
          </div>
        {/if}
        <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={plOn} onchange={(e) => (adv.plFilter = e.currentTarget.checked)} /><span>PL filter (receive)<span class="block text-xs text-muted">A 300 Hz high-pass that removes the PL tone from received audio. {sa818 ? 'Off by default for an SA818, which filters it itself.' : 'On by default.'}</span></span></label>
      </fieldset>

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
        <div class="grid gap-3 sm:grid-cols-2">{#each [...TIMER_FIELDS, ...DELAY_FIELDS] as f (f.key)}{@render num(f, adv, 'at')}{/each}</div>
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
