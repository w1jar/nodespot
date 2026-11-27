<script lang="ts">
  // The radio's timing and ID, and (collapsed) how its audio is wired: flat audio, the PL filter, the SA818 filters and the
  // microphone boost. Saved in station.advanced, station.id and (SA818 filters) station.radio; the node applies a save at once,
  // with no restart, except that the SA818 filters reach the radio only when Program is pressed. Empty means the default.
  // The audio levels and AGC are on the Audio tab.
  import { api } from '../../lib/api';
  import { isAdmin, refreshNode } from '../../lib/store.svelte';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { DELAY_FIELDS, ID_FIELDS, RADIO_ADV_KEYS, SA818_FILTER_KEYS, TIMER_FIELDS, audioBody, audioDirty, audioError, effectivePL, filterOn, programmedText, withKeys } from '../../lib/audio';
  import type { NumField } from '../../lib/audio';
  import { hasSA818, isShari, resolveInterface } from '../../lib/radio';
  import type { RadioInfo, RadioLevels, Sa818Filter, Station, StationAdvanced, StationId } from '../../lib/types';

  let { onsaved }: { onsaved?: () => void } = $props();
  let saved = $state<Station | null>(null), adv = $state<StationAdvanced>({}), id = $state<StationId>({}), filt = $state<Sa818Filter>({});
  let info = $state<RadioInfo | null>(null), lv = $state<RadioLevels | null>(null);
  let err = $state(''), busy = $state(false);
  const admin = $derived(isAdmin());
  const chk = 'mt-0.5 size-4 accent-[var(--accent)]';
  const dirty = $derived(!!saved && audioDirty(saved, adv, id, filt));
  // Without the radio info (an old node) the SA818 port decides, as in the radio card.
  const sa818 = $derived(saved ? (info ? hasSA818(resolveInterface(saved.radio, info.presets, isShari(info))) : !!saved.radio.sa818Port) : false);
  const plOn = $derived(effectivePL(adv, sa818));

  function fill(s: Station) { saved = s; adv = { ...s.advanced }; id = { ...s.id }; filt = { ...s.radio?.sa818Filter }; err = ''; }
  async function load() {
    try { fill(await api.station()); } catch (e) { toastError(e); }
    try { info = await api.radioInfo(); } catch { /* an old node has no info */ }
    try { lv = await api.radio(); } catch { /* the radio may be absent */ }
  }
  $effect(() => { load(); });

  const idOn = $derived(id.enabled !== false);
  const FLAG_LABELS = { emphasis: 'Pre/de-emphasis', highPass: 'High-pass (removes PL tone)', lowPass: 'Low-pass' } as const;
  const FLAG_HELP = { emphasis: 'Off by default.', highPass: 'On by default.', lowPass: 'On by default.' } as const;

  async function boost(on: boolean) { try { lv = await api.setRadio({ rxBoost: on }); } catch (e) { toastError(e); } }
  async function save(ev: Event) {
    ev.preventDefault();
    if (!saved) return;
    err = audioError(adv, id);
    if (err) return;
    busy = true;
    try {
      // Start from the station as saved now and change only this card's keys, so the Audio tab's settings are kept.
      const fresh = await api.station();
      await api.saveStation(audioBody(fresh, withKeys(fresh.advanced, adv, RADIO_ADV_KEYS), id, sa818 ? filt : undefined));
      await refreshNode(); await load(); onsaved?.(); toast('Radio settings saved');
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

<section class="card mt-4 space-y-4 p-4" aria-label="Timing and ID">
  <div class="flex flex-wrap items-center gap-2">
    <h2 class="text-base font-semibold">Timing and ID</h2>
    {#if dirty}<span class="badge" role="status">Unsaved changes</span>{/if}
  </div>
  {#if saved}
    <form class="space-y-4" onsubmit={save}>
      <fieldset class="space-y-4" disabled={!admin}>
        <fieldset class="space-y-3">
          <legend class="text-sm font-medium">Timers</legend>
          <p class="text-xs text-muted">When the transmitter keys and unkeys, and the longest transmission.</p>
          <div class="grid gap-3 sm:grid-cols-2">{#each [...TIMER_FIELDS, ...DELAY_FIELDS] as f (f.key)}{@render num(f, adv, 'at')}{/each}</div>
        </fieldset>

        <fieldset class="space-y-3">
          <legend class="text-sm font-medium">Identify</legend>
          <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={idOn} onchange={(e) => (id.enabled = e.currentTarget.checked)} /><span>Identify the station<span class="block text-xs text-muted">Sent on the radio only, after activity. The licensee is responsible for IDing.</span></span></label>
          <div class="grid gap-3 sm:grid-cols-3">{#each ID_FIELDS as f (f.key)}{@render num(f, id, 'at-id')}{/each}</div>
          <div>
            <label class="label" for="at-voice">Voice file</label>
            <input id="at-voice" class="input font-mono" bind:value={id.voiceFile} placeholder="empty: CW" autocomplete="off" spellcheck="false" />
            <p class="mt-1 text-xs text-muted">Absolute path of an 8 kHz mono WAV played instead of CW. If it cannot be read, the node sends CW.</p>
          </div>
        </fieldset>

        <details class="rounded border border-line" data-testid="radio-advanced">
          <summary class="cursor-pointer px-3 py-2 text-sm font-medium">Advanced</summary>
          <div class="space-y-4 border-t border-line p-3">
            <p class="text-xs text-muted">How the radio's audio is wired. The defaults suit a SHARI or a radio's speaker and mic jacks.</p>
            <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={!!adv.flatAudio} onchange={(e) => (adv.flatAudio = e.currentTarget.checked)} /><span>Radio audio is flat (data port or discriminator)<span class="block text-xs text-muted">The node adds 75 µs pre-emphasis to transmit and de-emphasis to receive. Off by default.</span></span></label>
            <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={plOn} onchange={(e) => (adv.plFilter = e.currentTarget.checked)} /><span>PL filter (receive)<span class="block text-xs text-muted">A 300 Hz high-pass that removes the PL tone from received audio. {sa818 ? 'Off by default for an SA818, which filters it itself.' : 'On by default.'}</span></span></label>
            {#if sa818}
              <fieldset class="space-y-2">
                <legend class="text-sm font-medium">SA818 filters</legend>
                <div class="grid gap-2 sm:grid-cols-3">
                  {#each SA818_FILTER_KEYS as k (k)}
                    <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={filterOn(filt, k)} onchange={(e) => (filt[k] = e.currentTarget.checked)} /><span>{FLAG_LABELS[k]}<span class="block text-xs text-muted">{FLAG_HELP[k]}</span></span></label>
                  {/each}
                </div>
                <p class="text-xs text-muted">Sent to the radio when you press Program. Saving alone does not change the radio.</p>
                <p class="text-xs text-muted" data-testid="sa818-programmed">{programmedText(info?.sa818Filters)}</p>
              </fieldset>
            {/if}
            {#if lv?.BoostAvailable}
              <label class="flex cursor-pointer items-start gap-2 text-sm"><input id="rt-boost" type="checkbox" class={chk} checked={!!lv.Boost} onchange={(e) => boost(e.currentTarget.checked)} /><span>Mic boost<span class="block text-xs text-muted">The sound chip's microphone boost on receive. Applies at once.</span></span></label>
            {/if}
          </div>
        </details>
      </fieldset>

      {#if err}<p class="text-sm text-danger" role="alert">{err}</p>{/if}
      {#if admin}
        <div class="flex flex-wrap items-center gap-2">
          <button class="btn btn-primary" type="submit" disabled={busy || !dirty}>{busy ? 'Saving…' : 'Save'}</button>
          <button class="btn btn-accent-outline" type="button" disabled={busy || !dirty} onclick={() => saved && fill(saved)}>Revert</button>
          <p class="text-xs text-muted">Applies at once. No restart needed. Empty fields use the default shown.</p>
        </div>
      {/if}
    </form>
  {:else}<p class="text-sm text-muted">Loading…</p>{/if}
</section>
