<script lang="ts">
  // The radio interface: the preset picker, the detected chip and the pin settings (advanced, collapsed).
  import type { RadioInfo, Station } from '../lib/types';
  import { COS_SOURCES, audioWarning, CTCSS_SOURCES, PTT_PINS, detectedLabel, findPreset, pickPreset, pinError, presetName, resolveInterface } from '../lib/radio';
  let { st = $bindable(), info, idPrefix = 'rp' }: { st: Station; info: RadioInfo; idPrefix?: string } = $props();
  const id = (k: string) => `${idPrefix}-${k}`;
  const chk = 'size-4 accent-[var(--accent)]';
  const name = $derived(presetName(st.radio.preset, info.shari));
  const preset = $derived(findPreset(info.presets, name));
  const iface = $derived(resolveInterface(st.radio, info.presets, info.shari));
  const found = $derived(detectedLabel(info));
  const bad = $derived(pinError(iface));
  const warn32 = $derived(audioWarning(info, iface));
  // The picker shows "Automatic" for an empty preset, so a node saved before presets existed keeps its behavior.
  const auto = $derived(info.shari ? 'SHARI' : 'None');
  function onPreset(v: string) { pickPreset(st.radio, v); }
</script>

<div class="space-y-3">
  <div class="max-w-md">
    <label class="label" for={id('preset')}>Radio interface</label>
    <select id={id('preset')} class="input" value={st.radio.preset ?? ''} onchange={(e) => onPreset(e.currentTarget.value)}>
      <option value="">Automatic ({auto})</option>
      {#each info.presets as p (p.name)}<option value={p.name}>{p.title}</option>{/each}
    </select>
    {#if preset}<p class="mt-1 text-xs text-muted">{preset.description}</p>{/if}
    {#if warn32}<p class="mt-2 text-sm text-warn" role="alert" data-testid="audio-unsupported">{warn32}</p>{/if}
    <p class="mt-1 text-xs text-muted" data-testid="detected">{found ? `Detected: ${found}${info.product ? ', ' + info.product : ''}` : 'No USB radio interface detected.'}</p>
  </div>
  {#if iface?.audio}
    <details class="rounded-md border border-border p-3">
      <summary class="cursor-pointer text-sm font-medium">Advanced: pins and polarity</summary>
      <div class="mt-3 space-y-3">
        <div class="grid gap-3 sm:grid-cols-3">
          <div>
            <label class="label" for={id('ptt')}>PTT pin</label>
            <select id={id('ptt')} class="input" value={iface.pttPin} onchange={(e) => (st.radio.pttGpio = +e.currentTarget.value)}>
              {#each PTT_PINS as n}<option value={n}>GPIO {n}</option>{/each}
            </select>
            <label class="mt-2 flex cursor-pointer items-center gap-2 text-sm"><input type="checkbox" class={chk} checked={iface.pttActiveLow} onchange={(e) => (st.radio.pttActiveLow = e.currentTarget.checked)} />Invert PTT (low transmits)</label>
          </div>
          <div>
            <label class="label" for={id('cos')}>Squelch (COS) input</label>
            <select id={id('cos')} class="input" value={iface.cos} onchange={(e) => (st.radio.cosSource = e.currentTarget.value)}>
              {#each COS_SOURCES as s}<option value={s.value}>{s.label}</option>{/each}
            </select>
            <label class="mt-2 flex cursor-pointer items-center gap-2 text-sm"><input type="checkbox" class={chk} checked={iface.cosActiveLow} disabled={iface.cos === 'none' || iface.cos === 'always'} onchange={(e) => (st.radio.cosActiveLow = e.currentTarget.checked)} />Invert COS (low is carrier)</label>
          </div>
          <div>
            <label class="label" for={id('ctcss')}>CTCSS (tone) input</label>
            <select id={id('ctcss')} class="input" value={iface.ctcss} onchange={(e) => (st.radio.ctcssSource = e.currentTarget.value)}>
              {#each CTCSS_SOURCES as s}<option value={s.value}>{s.label}</option>{/each}
            </select>
            <label class="mt-2 flex cursor-pointer items-center gap-2 text-sm"><input type="checkbox" class={chk} checked={iface.ctcssActiveLow} disabled={iface.ctcss === 'none'} onchange={(e) => (st.radio.ctcssActiveLow = e.currentTarget.checked)} />Invert CTCSS</label>
          </div>
        </div>
        <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class="mt-0.5 {chk}" checked={iface.sa818} onchange={(e) => (st.radio.sa818 = e.currentTarget.checked)} /><span>Program an SA818 radio module<span class="block text-xs text-muted">Off when the radio is set up by hand; the frequency and tone are then hidden.</span></span></label>
        {#if bad}<p class="text-sm text-danger" role="alert">{bad}</p>{/if}
        <p class="text-xs text-muted">Pin changes take effect after the node restarts, which it does by itself when you save.</p>
      </div>
    </details>
  {/if}
</div>
