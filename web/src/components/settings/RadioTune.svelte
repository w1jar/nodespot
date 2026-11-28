<script lang="ts">
  // The radio: the interface, the SA818 card (channel, volume, tail tone, filters), and the timing and ID. Whatever mode is active, audio goes to this radio.
  // The levels and AGC are on the Audio tab (AudioTune.svelte).
  import { api } from '../../lib/api';
  import { refreshNode } from '../../lib/store.svelte';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { stationBody, validateStation } from '../../lib/station';
  import { SA818_FILTER_KEYS, TAIL_CHOICES, filterOn, programmedText, sa818Body, sa818VolumeError, tailChoice, tailValue } from '../../lib/audio';
  import { hasSA818, isShari, pinError, radioBody, resolveInterface } from '../../lib/radio';
  import { MMDVM_BAUDS, MMDVM_DEFAULT_DEVICE, isMMDVM, mmdvmConfig, mmdvmError, mmdvmForm } from '../../lib/mmdvm';
  import type { RadioInfo, Station } from '../../lib/types';
  import StationFields from '../StationFields.svelte';
  import RadioPicker from '../RadioPicker.svelte';
  import RadioTimers from './RadioTimers.svelte';
  import ModemCard from './ModemCard.svelte';

  let st = $state<Station | null>(null), info = $state<RadioInfo | null>(null);
  let err = $state(''), busy = $state(false), prog = $state(false);
  const iface = $derived(st && info ? resolveInterface(st.radio, info.presets, isShari(info)) : undefined);
  // Without the info (an old node) the SA818 port decides, as before presets existed.
  const sa818 = $derived(info ? hasSA818(iface) : !!st?.radio.sa818Port);
  // The MMDVM hat: its settings are edited in a form of their own (frequencies in MHz) and saved as radio.mmdvm.
  const mmdvm = $derived(isMMDVM(iface?.preset ?? st?.radio.preset));
  let mf = $state(mmdvmForm(undefined));
  async function load() {
    try { const s = await api.station(); st = { ...s, radio: { ...s.radio }, id: { ...s.id } }; mf = mmdvmForm(s.radio.mmdvm); } catch (e) { toastError(e); }
    try { info = await api.radioInfo(); } catch { /* an old node has no info */ }
  }
  $effect(() => { load(); });

  async function save(ev: Event) {
    ev.preventDefault();
    if (!st) return;
    err = validateStation(st, sa818) || (sa818 ? sa818VolumeError(st.radio.sa818Volume) : '') || (info ? pinError(iface) : '') || (mmdvm ? mmdvmError(mf) : '');
    if (err) return;
    busy = true;
    try { const body = stationBody(st, sa818);
      if (sa818) body.radio = sa818Body(body.radio);
      if (info) body.radio = radioBody(body.radio, info.presets, isShari(info));
      if (mmdvm) body.radio.mmdvm = mmdvmConfig(mf);
      await api.saveStation(body); await refreshNode(); await load(); toast('Radio settings saved'); } catch (e) { toastError(e); } finally { busy = false; }
  }
  const chk = 'mt-0.5 size-4 accent-[var(--accent)]';
  const FLAG_LABELS = { emphasis: 'Pre/de-emphasis', highPass: 'High-pass (removes PL tone)', lowPass: 'Low-pass' } as const;
  const FLAG_HELP = { emphasis: 'Off by default.', highPass: 'On by default.', lowPass: 'On by default.' } as const;
  function setFilter(k: (typeof SA818_FILTER_KEYS)[number], on: boolean) { if (st) st.radio.sa818Filter = { ...st.radio.sa818Filter, [k]: on }; }
  async function program() {
    prog = true;
    try { await api.radioProgram(); toast('Radio programmed'); } catch (e) { toastError(e); } finally { prog = false; }
  }
</script>

{#snippet modemFields()}
  <div class="grid gap-3 sm:grid-cols-2">
    <div>
      <label class="label" for="rt-mm-device">Serial port</label>
      <input id="rt-mm-device" class="input font-mono" bind:value={mf.device} placeholder={MMDVM_DEFAULT_DEVICE} autocomplete="off" spellcheck="false" />
      <p class="mt-1 text-xs text-muted">Empty is {MMDVM_DEFAULT_DEVICE}, the Pi's UART on the GPIO header.</p>
    </div>
    <div>
      <label class="label" for="rt-mm-baud">Baud rate</label>
      <select id="rt-mm-baud" class="input" bind:value={mf.baud}>
        {#each MMDVM_BAUDS as b (b)}<option value={b}>{b}</option>{/each}
      </select>
      <p class="mt-1 text-xs text-muted">Most hats answer at 115200.</p>
    </div>
    <div>
      <label class="label" for="rt-mm-rx">Receive frequency (MHz)</label>
      <input id="rt-mm-rx" class="input tabular-nums" type="number" inputmode="decimal" min="100" max="1000" step="0.0001" bind:value={mf.rxMHz} />
    </div>
    <div>
      <label class="label" for="rt-mm-tx">Transmit frequency (MHz)</label>
      <input id="rt-mm-tx" class="input tabular-nums" type="number" inputmode="decimal" min="100" max="1000" step="0.0001" bind:value={mf.txMHz} />
    </div>
    <div>
      <label class="label" for="rt-mm-rxoff">Receive offset (Hz)</label>
      <input id="rt-mm-rxoff" class="input tabular-nums" type="number" inputmode="numeric" min="-100000" max="100000" step="1" bind:value={mf.rxOffsetHz} placeholder="0" />
    </div>
    <div>
      <label class="label" for="rt-mm-txoff">Transmit offset (Hz)</label>
      <input id="rt-mm-txoff" class="input tabular-nums" type="number" inputmode="numeric" min="-100000" max="100000" step="1" bind:value={mf.txOffsetHz} placeholder="0" />
    </div>
    <div>
      <label class="label" for="rt-mm-power">RF power (%)</label>
      <input id="rt-mm-power" class="input tabular-nums" type="number" inputmode="numeric" min="0" max="100" step="1" bind:value={mf.rfPower} placeholder="0" />
    </div>
    <div>
      <label class="label" for="rt-mm-cc">Color code</label>
      <input id="rt-mm-cc" class="input tabular-nums" type="number" inputmode="numeric" min="0" max="15" step="1" bind:value={mf.colorCode} placeholder="1" />
    </div>
    <div>
      <label class="label" for="rt-mm-gpio">Reset GPIO (optional)</label>
      <input id="rt-mm-gpio" class="input tabular-nums" type="number" inputmode="numeric" min="0" max="1023" step="1" bind:value={mf.resetGpio} placeholder="Not wired" />
    </div>
  </div>
  <p class="text-xs text-muted">The offsets trim the board's frequency error. Frequency, power and color code are stored for a later release; this preview does not send them to the hat.</p>
  <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} bind:checked={mf.duplex} /><span>Duplex (both DMR time slots)<span class="block text-xs text-muted">For a dual hat. On by default.</span></span></label>
  <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} bind:checked={mf.allowTx} data-testid="mm-allow-tx" /><span>Allow transmit<span class="block text-xs text-muted">Off by default, and turned off when a backup is restored. This preview never transmits, whatever this says.</span></span></label>
  <p class="text-xs text-muted">Saving a change here restarts the node to reopen the modem.</p>
{/snippet}

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
        <StationFields bind:st radio callsign={false} idToggle={false} idPrefix="rt" />
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <label class="label" for="rt-sa-volume">Volume (1 to 8)</label>
            <input id="rt-sa-volume" class="input tabular-nums" type="number" inputmode="numeric" min="1" max="8" step="1" bind:value={st.radio.sa818Volume} placeholder="8" />
            <p class="mt-1 text-xs text-muted">The module's speaker output level. Empty is 8.</p>
          </div>
          <div>
            <label class="label" for="rt-sa-tail">Tail tone</label>
            <select id="rt-sa-tail" class="input" value={tailChoice(st.radio.sa818Tail)} onchange={(e) => (st!.radio.sa818Tail = tailValue(e.currentTarget.value))}>
              {#each TAIL_CHOICES as c (c.value)}<option value={c.value}>{c.label}</option>{/each}
            </select>
            <p class="mt-1 text-xs text-muted">Open adds the module's squelch tail tone at the end of each transmission; Closed does not. Leave as is changes nothing.</p>
          </div>
        </div>
        <fieldset class="space-y-2">
          <legend class="text-sm font-medium">Filters</legend>
          <div class="grid gap-2 sm:grid-cols-3">
            {#each SA818_FILTER_KEYS as k (k)}
              <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={filterOn(st.radio.sa818Filter, k)} onchange={(e) => setFilter(k, e.currentTarget.checked)} /><span>{FLAG_LABELS[k]}<span class="block text-xs text-muted">{FLAG_HELP[k]}</span></span></label>
            {/each}
          </div>
        </fieldset>
        <p class="text-xs text-muted" data-testid="sa818-programmed">{programmedText(info ?? undefined)}</p>
      {:else if mmdvm}
        {@render modemFields()}
      {:else}
        <p class="text-sm text-muted">The frequency and tone are set on the radio itself. Save to keep the interface settings above.</p>
      {/if}
      {#if err}<p class="text-sm text-danger" role="alert">{err}</p>{/if}
      <div class="flex flex-wrap gap-2">
        <button class="btn btn-primary" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button>
        {#if sa818}<button class="btn" type="button" onclick={program} disabled={prog}>{prog ? 'Programming…' : 'Program the SA818'}</button>{/if}
      </div>
      {#if sa818}<p class="text-xs text-muted">{#if st.radio.sa818Port}SA818 on <span class="font-mono">{st.radio.sa818Port}</span>.{:else}The node finds the SA818's serial port itself.{/if} Saving programs the radio when a module value changed.</p>{/if}
    </form>
  </section>
{/if}

{#if st && mmdvm}<ModemCard />{/if}

<RadioTimers onsaved={load} />

<p class="mt-4 text-xs text-muted">Receive and transmit levels, the test tone and the AGC are on the <a class="text-accent underline" href="#/settings/station/audio">Audio</a> tab.</p>
