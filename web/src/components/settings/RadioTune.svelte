<script lang="ts">
  // The radio: the interface, the SA818 channel, and the timing and ID. Whatever mode is active, audio goes to this radio.
  // The levels and AGC are on the Audio tab (AudioTune.svelte).
  import { api } from '../../lib/api';
  import { refreshNode } from '../../lib/store.svelte';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { stationBody, validateStation } from '../../lib/station';
  import { hasSA818, isShari, pinError, radioBody, resolveInterface } from '../../lib/radio';
  import type { RadioInfo, Station } from '../../lib/types';
  import StationFields from '../StationFields.svelte';
  import RadioPicker from '../RadioPicker.svelte';
  import RadioTimers from './RadioTimers.svelte';

  let st = $state<Station | null>(null), info = $state<RadioInfo | null>(null);
  let err = $state(''), busy = $state(false), prog = $state(false);
  const iface = $derived(st && info ? resolveInterface(st.radio, info.presets, isShari(info)) : undefined);
  // Without the info (an old node) the SA818 port decides, as before presets existed.
  const sa818 = $derived(info ? hasSA818(iface) : !!st?.radio.sa818Port);
  async function load() {
    try { const s = await api.station(); st = { ...s, radio: { ...s.radio }, id: { ...s.id } }; } catch (e) { toastError(e); }
    try { info = await api.radioInfo(); } catch { /* an old node has no info */ }
  }
  $effect(() => { load(); });

  async function save(ev: Event) {
    ev.preventDefault();
    if (!st) return;
    err = validateStation(st, sa818) || (info ? pinError(iface) : '');
    if (err) return;
    busy = true;
    try { const body = stationBody(st, sa818);
      if (info) body.radio = radioBody(body.radio, info.presets, isShari(info));
      await api.saveStation(body); await refreshNode(); await load(); toast('Radio settings saved'); } catch (e) { toastError(e); } finally { busy = false; }
  }
  async function program() {
    prog = true;
    try { await api.radioProgram(); toast('Radio programmed'); } catch (e) { toastError(e); } finally { prog = false; }
  }
</script>

{#if st && info}
  <section class="card space-y-3 p-4" aria-label="Interface">
    <h2 class="text-base font-semibold">Interface</h2>
    <RadioPicker bind:st {info} idPrefix="rt" />
    {#if info.restartRequired}<p class="text-sm text-muted">The node restarts to use the saved interface settings.</p>{/if}
  </section>
{/if}

{#if st && (sa818 || info)}
  <section class="card mt-4 space-y-3 p-4" aria-label="Channel">
    <h2 class="text-base font-semibold">Channel</h2>
    <form class="space-y-3" onsubmit={save}>
      {#if sa818}
        <StationFields bind:st radio idToggle={false} idPrefix="rt" />
      {:else}
        <p class="text-sm text-muted">The frequency and tone are set on the radio itself. Save to keep the interface settings above.</p>
      {/if}
      {#if err}<p class="text-sm text-danger" role="alert">{err}</p>{/if}
      <div class="flex flex-wrap gap-2">
        <button class="btn btn-primary" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
        {#if sa818 && st.radio.sa818Port}<button class="btn" type="button" onclick={program} disabled={prog}>{prog ? 'Programming…' : 'Program the SA818'}</button>{/if}
      </div>
      {#if sa818 && st.radio.sa818Port}<p class="text-xs text-muted">SA818 on <span class="font-mono">{st.radio.sa818Port}</span>. Saving programs the channel when the radio is present.</p>{/if}
    </form>
  </section>
{/if}

<RadioTimers onsaved={load} />

<p class="mt-4 text-xs text-muted">Receive and transmit levels, the test tone and the AGC are on the <a class="text-accent underline" href="#/settings/station/audio">Audio</a> tab.</p>
