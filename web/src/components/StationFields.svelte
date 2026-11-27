<script lang="ts">
  // The station form shared by the first-run setup and Settings: callsign, radio channel and tone, ID on or off.
  import type { Station } from '../lib/types';
  import { CTCSS } from '../lib/station';
  let { st = $bindable(), radio = true, idToggle = true, idPrefix = 'st' }: { st: Station; radio?: boolean; idToggle?: boolean; idPrefix?: string } = $props();
  const id = (k: string) => `${idPrefix}-${k}`;
  const chk = 'size-4 accent-[var(--accent)]';
  let idOn = $state(st.id.enabled !== false);
  $effect(() => { if (idToggle) st.id.enabled = idOn; });
</script>

<div class="space-y-3">
  <div class="max-w-xs">
    <label class="label" for={id('call')}>Station callsign</label>
    <input id={id('call')} class="input font-mono uppercase" bind:value={st.callsign} autocomplete="off" placeholder="N0CALL" required />
  </div>
  {#if radio}
    <div class="grid gap-3 sm:grid-cols-3">
      <div>
        <label class="label" for={id('freq')}>Frequency (MHz)</label>
        <input id={id('freq')} class="input tabular-nums" type="number" step="0.0005" min="100" max="500" bind:value={st.radio.frequencyMHz} placeholder="146.5200" />
      </div>
      <div>
        <label class="label" for={id('tone')}>CTCSS / DCS</label>
        <input id={id('tone')} class="input font-mono" list={id('tones')} bind:value={st.radio.tone} placeholder="none" autocomplete="off" />
        <datalist id={id('tones')}>{#each CTCSS as t}<option value={t}></option>{/each}</datalist>
        <p class="mt-1 text-xs text-muted">A tone such as 100.0 or a DCS code such as D023N. Empty for none.</p>
      </div>
      <div>
        <label class="label" for={id('sq')}>Squelch (0 to 8)</label>
        <input id={id('sq')} class="input tabular-nums" type="number" min="0" max="8" bind:value={st.radio.squelch} placeholder="4" />
      </div>
    </div>
    <label class="flex cursor-pointer items-center gap-2 text-sm"><input type="checkbox" class={chk} bind:checked={st.radio.narrow} />Narrow channel (12.5 kHz)</label>
  {/if}
  {#if idToggle}<label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class="mt-0.5 {chk}" bind:checked={idOn} /><span>Identify the station<span class="block text-xs text-muted">The licensee is responsible for IDing.</span></span></label>{/if}
</div>
